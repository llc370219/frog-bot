package onefrog

import (
	"slices"
	"strings"
	"testing"
)

func TestCLIArgsPassModelOnlyAsSeparateArgument(t *testing.T) {
	for _, id := range []string{"codex", "claude", "pi"} {
		args, _ := cliArgs(id, "task", "my-model")
		i := slices.Index(args, "--model")
		if i < 0 || args[i+1] != "my-model" {
			t.Fatalf("%s: model flag missing: %v", id, args)
		}
		if plain, _ := cliArgs(id, "task", ""); slices.Contains(plain, "--model") {
			t.Fatalf("%s: model flag without model: %v", id, plain)
		}
	}
	if args, _ := cliArgs("codex", "task", "m"); args[len(args)-1] != "-" {
		t.Fatalf("codex must still read the prompt from stdin: %v", args)
	}
}

func TestValidateCLIOptions(t *testing.T) {
	o, err := ValidateCLIOptions("claude", CLIOptions{Model: " opus "})
	if err != nil || o.Model != "opus" || o.Auth != CLIAuthSubscription {
		t.Fatalf("got %+v, %v", o, err)
	}
	for _, bad := range []string{"--dangerously-skip-permissions", "a b", "-m", strings.Repeat("x", 101)} {
		if _, err := ValidateCLIOptions("claude", CLIOptions{Model: bad}); err == nil {
			t.Fatalf("accepted model %q", bad)
		}
	}
	if _, err := ValidateCLIOptions("claude", CLIOptions{Auth: "token"}); err == nil {
		t.Fatal("accepted unknown auth mode")
	}
	if o, _ := ValidateCLIOptions("grok", CLIOptions{Model: "grok-4"}); o.Model != "" {
		t.Fatal("model kept for a CLI without a model option")
	}
}

func TestCLIEnvSubscriptionDropsAPIKeys(t *testing.T) {
	environ := []string{"PATH=/bin", "HOME=/home/u", "ANTHROPIC_API_KEY=sk-ant", "anthropic_base_url=https://x", "OPENAI_API_KEY=sk-oa", "FROGBOT_MASTER_KEY=m"}
	sub := strings.Join(cliEnv(environ, "claude", CLIAuthSubscription), "\n")
	if strings.Contains(sub, "sk-ant") || strings.Contains(sub, "https://x") || strings.Contains(sub, "FROGBOT_MASTER_KEY") ||
		!strings.Contains(sub, "PATH=/bin") || !strings.Contains(sub, "sk-oa") {
		t.Fatalf("subscription env:\n%s", sub)
	}
	env := strings.Join(cliEnv(environ, "claude", CLIAuthEnv), "\n")
	if !strings.Contains(env, "sk-ant") || strings.Contains(env, "FROGBOT_MASTER_KEY") {
		t.Fatalf("env mode:\n%s", env)
	}
}
