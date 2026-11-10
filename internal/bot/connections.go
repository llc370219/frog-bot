package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"frogbot/internal/onefrog"
)

type agentConnection struct {
	CLI string `json:"cli"` // Empty selects the built-in agent.
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
	if err := json.Unmarshal(raw, &b.connection); err != nil {
		return err
	}
	if id := b.connection.CLI; id != "" && onefrog.CLIName(id) == "" {
		return fmt.Errorf("未知的 Agent CLI：%s", id)
	}
	return nil
}

func (b *Bot) selectedCLI() string {
	b.modelMu.RLock()
	defer b.modelMu.RUnlock()
	return b.connection.CLI
}

func (b *Bot) connections() connectionsView {
	return connectionsView{agentConnection{CLI: b.selectedCLI()}, onefrog.DetectCLIs()}
}

func (b *Bot) saveAgentConnection(id string) error {
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
	b.modelMu.Lock()
	defer b.modelMu.Unlock()
	if b.connection.CLI == id {
		return nil
	}
	draft := agentConnection{CLI: id}
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
