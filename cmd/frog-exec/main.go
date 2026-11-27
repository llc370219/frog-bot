// Command frog-exec is the low-privilege controlled execution bridge for Frog Bot V2.
// Scripts and Coding CLIs invoke `frog-exec <cmd> [args...]` with {{secret://name}} handles;
// the Host Daemon adjudicates access via the Security Gateway, injects secrets in isolated
// RAM, runs the command, and returns multi-encoding scrubbed output.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"frogbot/internal/security"
)

func main() {
	sock := flag.String("sock", os.Getenv("FROG_EXEC_SOCK"), "Host Daemon frog-exec Unix socket path")
	botID := flag.String("bot", envOr("FROG_BOT_ID", "main"), "Target Bot ID")
	timeout := flag.Int("timeout", 120, "Command timeout in seconds")
	tainted := flag.Bool("tainted", os.Getenv("FROG_TAINTED") == "1", "Propagate taint flag")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: frog-exec [--sock /data/host/frog-exec.sock] [--bot main] -- <command> [args...]")
		os.Exit(2)
	}
	if strings.TrimSpace(*sock) == "" {
		*sock = "/data/host/frog-exec.sock"
	}

	cwd, _ := os.Getwd()
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.Contains(v, "{{secret:") {
			env[k] = v
		}
	}
	resp, err := security.RunExecClient(*sock, security.ExecRequest{
		BotID:         *botID,
		Cwd:           cwd,
		Command:       args[0],
		Args:          args[1:],
		Env:           env,
		TimeoutSec:    *timeout,
		Tainted:       *tainted,
		TriggerSource: "cli:frog-exec",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(126)
	}
	if resp.Stdout != "" {
		_, _ = os.Stdout.WriteString(resp.Stdout)
	}
	if resp.Stderr != "" {
		_, _ = os.Stderr.WriteString(resp.Stderr)
	}
	os.Exit(resp.ExitCode)
}

func envOr(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return fallback
}
