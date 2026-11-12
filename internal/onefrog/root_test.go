package onefrog

import (
	"context"
	"testing"
)

type fakeRoot struct {
	available bool
	ran       []string
}

func (f *fakeRoot) RootAvailable() bool { return f.available }
func (f *fakeRoot) RunAsRoot(_ context.Context, cmd string, _ int, _ string) (string, int, error) {
	f.ran = append(f.ran, cmd)
	return "ok", 0, nil
}

// run_as_root 在任何审批模式下都只能由用户批准：规则、审核模型、never 模式都不能放行。
func TestRootToolAlwaysAsksUser(t *testing.T) {
	args := raw(map[string]string{"command": "apt-get install -y libatomic1", "reason": "Node 需要"})
	noop := func(EventType, any) {}
	for _, p := range []Policy{PolicyUntrusted, PolicyOnRequest, PolicyAuto} {
		g, _ := NewGuard(Rules{AllowShell: []string{`.*`}}, stubReviewer{Verdict{Decision: Allow}}, nil)
		task := Task{ID: "t", Policy: p, Workspace: t.TempDir()}
		if v, _ := g.Classify(task, rootTool, args); v.Decision != AskUser {
			t.Errorf("%s: classify %v", p, v)
		}
		if v := g.Check(context.Background(), task, rootTool, args, noop); v.Decision != Deny {
			t.Errorf("%s: without a user it must be denied, got %v", p, v)
		}
	}
	g, _ := NewGuard(Rules{}, nil, fixedApprover{UserDecision{Approve: true}})
	if v := g.Check(context.Background(), Task{ID: "t", Policy: PolicyNever}, rootTool, args, noop); v.Decision != Deny {
		t.Errorf("never policy must deny root, got %v", v)
	}
}

// 用户点了「本任务内都允许」，root 命令也只放行这一次。
func TestRootApprovalIsNeverSessionScoped(t *testing.T) {
	args := raw(map[string]string{"command": "systemctl restart nginx", "reason": "x"})
	noop := func(EventType, any) {}
	g, _ := NewGuard(Rules{}, nil, fixedApprover{UserDecision{Approve: true, Scope: ScopeSession}})
	task := Task{ID: "t", Policy: PolicyOnRequest, Workspace: t.TempDir()}
	if v := g.Check(context.Background(), task, rootTool, args, noop); v.Decision != Allow {
		t.Fatalf("first: %v", v)
	}
	if _, ok := g.Consume("t", RootTool, ArgsHash(args)); ok {
		t.Fatal("root approval must not be reusable")
	}
}

func TestRootToolOnlyListedWhenAvailable(t *testing.T) {
	has := func() bool {
		for _, tl := range DefaultTools() {
			if tl.Name() == RootTool {
				return true
			}
		}
		return false
	}
	defer SetRootRunner(nil)
	SetRootRunner(nil)
	if has() {
		t.Fatal("no runner: tool must be hidden")
	}
	r := &fakeRoot{}
	SetRootRunner(r)
	if has() {
		t.Fatal("runner unavailable: tool must be hidden")
	}
	r.available = true
	if !has() {
		t.Fatal("runner available: tool must be listed")
	}
	out, err := rootTool.Execute(context.Background(), ToolEnv{TaskID: "t"}, raw(map[string]string{"command": "id", "reason": "check"}))
	if err != nil || len(r.ran) != 1 || r.ran[0] != "id" || out == "" {
		t.Fatalf("run: %q %v %v", out, err, r.ran)
	}
}
