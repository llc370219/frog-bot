package onefrog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CLIGateway 是 frog 的本机模型网关（见 bot/gateway.go）：给一次 CLI 运行发临时令牌，
// 令牌只能用设置里选的那个 API 渠道和模型，真实 Key 留在 frog 里。
type CLIGateway interface {
	Issue(channel, model string, ttl time.Duration) (baseURL, token string, revoke func(), err error)
}

var cliGatewayCfg struct {
	sync.RWMutex
	g CLIGateway
}

// SetCLIGateway 由 Bots 启动时设置。
func SetCLIGateway(g CLIGateway) {
	cliGatewayCfg.Lock()
	cliGatewayCfg.g = g
	cliGatewayCfg.Unlock()
}

func currentCLIGateway() CLIGateway {
	cliGatewayCfg.RLock()
	defer cliGatewayCfg.RUnlock()
	return cliGatewayCfg.g
}

// piProvider 是写进 Pi 临时配置里的提供方名字。
const piProvider = "frog"

// piChannelEnv 给这一次 Pi 运行准备临时配置目录（PI_CODING_AGENT_DIR）：models.json 里只有一个
// 指向本机网关的提供方，Key 写成 $FROG_GATEWAY_TOKEN，令牌放在环境变量里。运行结束 cleanup 作废令牌、删目录。
func piChannelEnv(opts CLIOptions) (env []string, cleanup func(), err error) {
	if opts.Channel == "" || opts.Model == "" {
		return nil, nil, errors.New("还没有给 Pi 选择 API 渠道和模型：请在「设置 → 模型与 Agent」里选择")
	}
	gw := currentCLIGateway()
	if gw == nil {
		return nil, nil, errors.New("frog 的本机模型网关没有启动")
	}
	base, token, revoke, err := gw.Issue(opts.Channel, opts.Model, 6*time.Hour)
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "frog-pi-")
	if err != nil {
		revoke()
		return nil, nil, err
	}
	cleanup = func() { revoke(); _ = os.RemoveAll(dir) }
	cfg := map[string]any{"providers": map[string]any{piProvider: map[string]any{
		"baseUrl": base,
		"api":     "openai-completions",
		"apiKey":  "$FROG_GATEWAY_TOKEN",
		// 第三方中转站常常不认 developer 角色和 reasoning_effort，用最通用的写法
		"compat": map[string]bool{"supportsDeveloperRole": false, "supportsReasoningEffort": false},
		"models": []map[string]any{{"id": opts.Model, "name": opts.Model}},
	}}}
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "models.json"), raw, 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	return []string{"PI_CODING_AGENT_DIR=" + dir, "FROG_GATEWAY_TOKEN=" + token}, cleanup, nil
}

// withoutEnv 去掉指定的环境变量（例如服务器上原有的 PI_CODING_AGENT_DIR）。
func withoutEnv(env []string, keys ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, d := range keys {
			if strings.EqualFold(k, d) {
				drop = true
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}
