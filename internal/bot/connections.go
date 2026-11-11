package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"frogbot/internal/onefrog"
)

// agentConnection 是每个 Bot 自己的 Agent 执行方式（data/…/agent-connection.json）。
// CLI 为空时用内置 Agent（走「Agent API 模型」）；否则用该 CLI 以及它在这台设备上登录的账户。
type agentConnection struct {
	CLI string `json:"cli"` // Empty selects the built-in agent.
	onefrog.CLIOptions
}

type connectionsView struct {
	agentConnection
	CLIs []onefrog.CLIStatus `json:"clis"`
}

func (b *Bot) connectionPath() string { return filepath.Join(b.cfg.DataDir, "agent-connection.json") }

func (b *Bot) loadAgentConnection() error {
	raw, err := os.ReadFile(b.connectionPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var c agentConnection
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	if id := c.CLI; id != "" && onefrog.CLIName(id) == "" {
		return fmt.Errorf("未知的 Agent CLI：%s", id)
	}
	if c.CLIOptions, err = onefrog.ValidateCLIOptions(c.CLI, c.CLIOptions); err != nil {
		return err
	}
	b.connection = c
	return nil
}

func (b *Bot) selectedCLI() string {
	b.modelMu.RLock()
	defer b.modelMu.RUnlock()
	return b.connection.CLI
}

// cliOptions 返回指定 CLI 当前的模型与认证设置；不是当前所选 CLI 时用默认（官方订阅、CLI 默认模型）。
func (b *Bot) cliOptions(id string) onefrog.CLIOptions {
	b.modelMu.RLock()
	defer b.modelMu.RUnlock()
	if id != "" && id == b.connection.CLI {
		return b.connection.CLIOptions
	}
	return onefrog.CLIOptions{Auth: onefrog.CLIAuthSubscription}
}

func (b *Bot) connections() connectionsView {
	b.modelMu.RLock()
	c := b.connection
	b.modelMu.RUnlock()
	if c.CLI != "" && c.Auth == "" {
		c.Auth = onefrog.CLIAuthSubscription
	}
	return connectionsView{c, onefrog.DetectCLIs()}
}

func (b *Bot) saveAgentConnection(draft agentConnection) error {
	id := draft.CLI
	if id != "" {
		if onefrog.CLIName(id) == "" {
			return errors.New("请选择支持的 Agent CLI")
		}
		for _, cli := range onefrog.DetectCLIs() {
			if cli.ID == id && !cli.Installed {
				return fmt.Errorf("未检测到 %s，请先安装并加入 Frog Bot 所在机器的 PATH", cli.Name)
			}
		}
	}
	opts, err := onefrog.ValidateCLIOptions(id, draft.CLIOptions)
	if err != nil {
		return err
	}
	draft.CLIOptions = opts
	b.modelMu.Lock()
	defer b.modelMu.Unlock()
	if b.connection == draft {
		return nil
	}
	tmp, err := os.CreateTemp(b.cfg.DataDir, ".agent-connection-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = json.NewEncoder(tmp).Encode(draft)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), b.connectionPath()); err != nil {
		return err
	}
	b.connection = draft
	return nil
}
