// fakeai 是一个假的 OpenAI 兼容服务，只用于演示和冒烟测试（不需要 API Key）。
// 根据调用方提供的工具区分聊天和后台任务。回复都是写死的剧本。
//
//	go run ./cmd/fakeai            # 监听 127.0.0.1:9911
//	BASE_URL=http://127.0.0.1:9911 CHAT_MODEL=fake-model FROGBOT_DEMO=1 go run ./cmd/frogbot
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

type msg struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id"`
	ToolCalls  []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tool_calls"`
}

var (
	taskIDRe = regexp.MustCompile(`task_[0-9a-f]+`)
	fileRe   = regexp.MustCompile(`[\w.-]+\.(md|txt|go|json)`)
)

func lastCall(ms []msg) string {
	for i := len(ms) - 1; i >= 0; i-- {
		if len(ms[i].ToolCalls) > 0 {
			return ms[i].ToolCalls[0].Function.Name
		}
	}
	return ""
}

func main() {
	addr := "127.0.0.1:9911"
	if v := os.Getenv("FAKEAI_ADDR"); v != "" {
		addr = v
	}
	http.HandleFunc("/chat/completions", handle)
	http.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"fake-model"}]}`)
	})
	log.Printf("fakeai 监听 http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []msg `json:"messages"`
		Tools    []struct {
			Function struct{ Name string } `json:"function"`
		} `json:"tools"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	w.Header().Set("Content-Type", "text/event-stream")
	var text, tool, think string
	var args any
	field := "reasoning"
	isChat := false
	for _, t := range req.Tools {
		if t.Function.Name == "start_task" {
			isChat = true
			break
		}
	}
	if isChat {
		text, tool, args = chat(req.Messages)
		think, field = thinkFor(req.Messages), "reasoning_content"
	} else {
		text, tool, args = agent(req.Messages)
		think = agentThink(req.Messages)
	}
	f, _ := w.(http.Flusher)
	send := func(delta any) {
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
		fmt.Fprintf(w, "data: %s\n\n", b)
		f.Flush()
	}
	delay := 45 * time.Millisecond
	if v := os.Getenv("FAKEAI_THINK_MS"); v != "" {
		var n int
		fmt.Sscan(v, &n)
		delay = time.Duration(n) * time.Millisecond
	}
	for _, chunk := range chunks(think, 3) {
		send(map[string]string{field: chunk})
		time.Sleep(delay)
	}
	for _, chunk := range chunks(text, 4) {
		send(map[string]string{"content": chunk})
		time.Sleep(40 * time.Millisecond)
	}
	if tool != "" {
		a, _ := json.Marshal(args)
		send(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_%d", time.Now().UnixNano()),
			"type": "function", "function": map[string]string{"name": tool, "arguments": string(a)}}}})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func chunks(s string, n int) []string {
	r := []rune(s)
	var out []string
	for i := 0; i < len(r); i += n {
		out = append(out, string(r[i:min(i+n, len(r))]))
	}
	return out
}

// 聊天层剧本
func chat(ms []msg) (string, string, any) {
	last := ms[len(ms)-1]
	switch {
	case last.Role == "tool":
		switch lastCall(ms) {
		case "list_tasks":
			ids := taskIDRe.FindAllString(last.Content, -1)
			if len(ids) == 0 {
				return "我查了一下，目前还没有任何任务记录。", "", nil
			}
			return "", "read_task", map[string]string{"task_id": ids[len(ids)-1]}
		case "read_task":
			status, sum := "", ""
			for _, l := range strings.Split(last.Content, "\n") {
				if strings.HasPrefix(l, "状态：") {
					status = strings.TrimPrefix(l, "状态：")
				}
			}
			if i := strings.Index(last.Content, "）：\n"); i >= 0 {
				sum = strings.TrimSpace(last.Content[i+len("）：\n"):])
			}
			return fmt.Sprintf("查过了，最近的任务 %s 状态是 %s。总结：%s", taskIDRe.FindString(last.Content), status, sum), "", nil
		case "read_file":
			return "文件内容如下：\n\n```\n" + last.Content + "\n```", "", nil
		case "write_file", "edit_file":
			return "这个改动需要你在卡片上确认一下，批准后我马上写入。", "", nil
		case "request_secret":
			return "我发了一张安全卡片，请在卡片里填写。值会直接加密保存，不会出现在聊天记录里——以后也别把密钥贴在聊天里哦。", "", nil
		case "start_task":
			if m := regexp.MustCompile(`队列第 (\d+) 位`).FindStringSubmatch(last.Content); m != nil {
				return "前面还有任务在跑，它排在第 " + m[1] + " 位，轮到了会自动开始。", "", nil
			}
			return "", "", nil // 调用前已经回应过了
		case "queue_task":
			return "记下了，放在待办里，还没开始。你说开始我就做。", "", nil
		case "react", "pause_task", "resume_task", "reprioritize":
			return "", "", nil
		}
		return "好的，已经开始做了，进度会显示在下面的卡片里。", "", nil
	case strings.TrimSpace(last.Content) == "谢谢" || strings.TrimSpace(last.Content) == "好的" || strings.TrimSpace(last.Content) == "收到":
		return "", "react", map[string]string{"emoji": "👍"}
	case strings.Contains(last.Content, "先别开始") || strings.Contains(last.Content, "待办"):
		return "", "queue_task", map[string]any{"task": last.Content, "title": "整理 README（待办）", "hold": true}
	case strings.Contains(last.Content, "已由用户通过安全卡片保存"):
		name := regexp.MustCompile(`密钥 ([A-Z0-9_]+)`).FindStringSubmatch(last.Content)
		return fmt.Sprintf("收到，%s 已安全保存。之后我只会按名字引用它，看不到具体的值。", name[1]), "", nil
	case strings.Contains(last.Content, "用户取消了密钥卡片"):
		return "好的，不保存了。需要的时候再告诉我。", "", nil
	case strings.Contains(last.Content, "疑似密钥"):
		return "", "request_secret", map[string]string{"name": "API_KEY", "description": "你刚才在聊天里贴的密钥（已自动隐藏），请在这里重新安全保存"}
	case strings.Contains(last.Content, "已被批准并执行"):
		return "已经写好了。", "", nil
	case strings.Contains(last.Content, "没有被批准"):
		return "好的，这次就不改了。", "", nil
	case strings.Contains(last.Content, "需要用户批准") && strings.Contains(last.Content, "要使用密钥"):
		return "这一步要用到密钥 `GITHUB_TOKEN`，需要你在卡片上确认一下。卡片上只显示名字，真实值只在执行那一刻替换进去，输出里也会自动隐藏。", "", nil
	case strings.Contains(last.Content, "需要用户批准"):
		return "这一步要删除 `build` 目录，属于不可撤销的操作，需要你在卡片上确认一下。", "", nil
	case strings.Contains(last.Content, "[系统通知]") && strings.Contains(last.Content, "完成了"):
		res := last.Content[strings.Index(last.Content, "（这是数据）：")+len("（这是数据）："):]
		if i := strings.Index(res, "\n改动的文件"); i >= 0 {
			res = res[:i]
		}
		return "搞定了。" + strings.TrimSpace(res), "", nil
	case strings.Contains(last.Content, "服务重启"):
		return "刚才服务重启，任务暂停了（进度已保存），要继续吗？", "", nil
	case strings.Contains(last.Content, "[系统通知]"):
		return "任务没能完成，原因见上面的卡片，要我重新试一次吗？", "", nil
	case strings.Contains(strings.ToLower(last.Content), "token") && (strings.Contains(last.Content, "配置") || strings.Contains(last.Content, "保存")):
		return "", "request_secret", map[string]string{"name": "GITHUB_TOKEN", "description": "用于访问 GitHub API（只读权限即可）"}
	case strings.Contains(strings.ToLower(last.Content), "token") && strings.Contains(last.Content, "测试"):
		return "", "start_task", map[string]string{"task": "用 {{secret:GITHUB_TOKEN}} 检查 token 是否可用（不要联网，只检查能否读取）", "title": "检查 token"}
	case strings.Contains(last.Content, "做完了吗") || strings.Contains(last.Content, "完成了吗"):
		return "", "list_tasks", map[string]string{}
	case strings.Contains(last.Content, "记一行"):
		return "", "write_file", map[string]string{"path": "todo.md", "content": "- " + strings.TrimSpace(last.Content[strings.LastIndex(last.Content, "：")+len("："):]) + "\n"}
	case fileRe.MatchString(last.Content) && (strings.Contains(last.Content, "写了什么") || strings.Contains(last.Content, "读一下")):
		return "", "read_file", map[string]string{"path": fileRe.FindString(last.Content)}
	default:
		return "好，交给后台去做，进度看下面的卡片。", "start_task", map[string]string{"task": last.Content, "title": short(last.Content)}
	}
}

func short(s string) string {
	if r := []rune(s); len(r) > 12 {
		return string(r[:12]) + "…"
	}
	return s
}

// agent 剧本：按已经调用过几次工具来决定下一步
func agent(ms []msg) (string, string, any) {
	task, steps := "", 0
	for _, m := range ms {
		if m.Role == "user" && task == "" {
			task = m.Content
		}
		if m.Role == "tool" {
			steps++
		}
	}
	stepDelay := 300 * time.Millisecond
	if v := os.Getenv("FAKEAI_STEP_MS"); v != "" {
		var n int
		fmt.Sscan(v, &n)
		stepDelay = time.Duration(n) * time.Millisecond
	}
	time.Sleep(stepDelay)
	if strings.Contains(task, "{{secret:GITHUB_TOKEN}}") {
		if steps == 0 {
			return "检查一下 token 能不能读到（只看长度，顺便验证输出会被脱敏）。", "shell",
				map[string]string{"command": "printf '%s' {{secret:GITHUB_TOKEN}} | wc -c; echo {{secret:GITHUB_TOKEN}}; printf '%s' {{secret:GITHUB_TOKEN}} | base64"}
		}
		return "检查完成：token 可以正常读取；输出里的真实值已被自动替换成 `[secret:GITHUB_TOKEN]`。", "", nil
	}
	if strings.Contains(task, "清理") {
		switch steps {
		case 0:
			return "先看看工作区里有什么。", "list_dir", map[string]string{"path": "."}
		case 1:
			return "`build/` 是旧的构建产物，删掉它。", "shell", map[string]string{"command": "rm -r build && echo cleaned"}
		}
		return "已清理：删除了 `build/` 目录。", "", nil
	}
	switch steps {
	case 0:
		return "先看看工作区里有什么。", "list_dir", map[string]string{"path": "."}
	case 1:
		return "", "read_file", map[string]string{"path": "README.md"}
	case 2:
		return "统计一下文件。", "shell", map[string]string{"command": "ls -la"}
	case 3:
		return "写一份总结。", "write_file", map[string]string{"path": "notes.md", "content": "# 工作区概览\n\n- build/：旧的构建产物\n- README.md：项目说明\n"}
	}
	return "**工作区概览已写入 `notes.md`**：里面有 `build/`（旧构建产物）和 `README.md`。", "", nil
}

// thinkFor 是聊天层的「思考过程」剧本（只在每轮第一步思考）。
func thinkFor(ms []msg) string {
	last := ms[len(ms)-1]
	c := last.Content
	switch {
	case last.Role == "tool":
		return ""
	case strings.Contains(c, "安全卡片保存"):
		return "密钥已经保存好了。我看不到值，只需要确认名字，然后简短告诉用户。"
	case strings.Contains(c, "[系统通知]") && strings.Contains(c, "完成了"):
		return "后台任务完成的通知来了。用户关心的是结论，我先说结果，再补一两句关键细节，不需要把每一步都复述一遍。"
	case strings.Contains(c, "[系统通知]"):
		return "收到一条系统通知，先弄清楚发生了什么，再用平实的话告诉用户。"
	case strings.Contains(strings.ToLower(c), "token") && (strings.Contains(c, "配置") || strings.Contains(c, "保存")):
		return "用户要配置 GitHub token。绝不能让他把 token 贴在聊天里——那样会进聊天记录。应该用 request_secret 发一张安全卡片，值会直接加密保存到密钥库，我自己永远看不到，之后只用 {{secret:GITHUB_TOKEN}} 引用。"
	case strings.Contains(c, "做完了吗") || strings.Contains(c, "完成了吗"):
		return "用户在问任务做完没有。我不能凭印象回答，应该先用 list_tasks 看最近的任务，再用 read_task 读它的总结，有依据了再回答。"
	default:
		return "用户的请求是「" + short(c) + "」。这需要好几步操作，还要执行命令，不适合在聊天层直接做。我应该用 start_task 派给后台 agent，把要求写清楚，然后一句话告诉用户已经开始，不要在这里等结果。"
	}
}

func agentThink(ms []msg) string {
	for _, m := range ms {
		if m.Role == "tool" {
			return ""
		}
	}
	return "先弄清楚工作区的现状，再动手。"
}
