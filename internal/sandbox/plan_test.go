package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlanCarvesOutProtectedDir(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("etc")
	mk("var/lib/frogbot/data/tools/node")
	mk("var/lib/frogbot/data/bots")
	mk("var/lib/frogbot/workspace")
	mk("var/lib/frogbot/.nvm")
	mk("var/log")
	if err := os.WriteFile(filepath.Join(root, "var/lib/frogbot/.bashrc"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 指向受保护目录和它上级的链接都不能开放
	if err := os.Symlink(filepath.Join(root, "var/lib/frogbot/data"), filepath.Join(root, "data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "var"), filepath.Join(root, "var-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "etc"), filepath.Join(root, "etc-link")); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "var/lib/frogbot/data")
	tools := filepath.Join(data, "tools")
	got := map[string]uint64{}
	for _, p := range planFrom(root, Spec{Protect: []string{data}, Allow: []Rule{{tools, ReadOnly}}}) {
		rel, _ := filepath.Rel(root, p.path)
		got[rel] |= p.access
	}
	all := uint64(1<<16 - 1)
	want := map[string]uint64{
		".":                          accessReadDir,
		"etc":                        all,
		"etc-link":                   all,
		"var":                        ancestorRights,
		"var/log":                    all,
		"var/lib":                    ancestorRights,
		"var/lib/frogbot":            ancestorRights,
		"var/lib/frogbot/workspace":  all,
		"var/lib/frogbot/.nvm":       all,
		"var/lib/frogbot/.bashrc":    all,
		"var/lib/frogbot/data/tools": readRights,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: access %b, want %b", k, got[k], v)
		}
	}
	for _, bad := range []string{"data", "var-link", "var/lib/frogbot/data", "var/lib/frogbot/data/bots"} {
		if _, ok := got[bad]; ok {
			t.Errorf("%s must not get a rule", bad)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected rules: %v", got)
	}
	// 上级目录的权限里不能有读文件、写文件、删除、建链接
	if ancestorRights&(accessReadFile|accessWriteFile|accessRemoveFile|accessRemoveDir|accessMakeSym|accessRefer|accessTruncate) != 0 {
		t.Fatal("ancestor rights too broad")
	}
}

func TestNormalizeRejectsRoot(t *testing.T) {
	if _, err := (Spec{Protect: []string{"/"}}).Normalize(); err == nil {
		t.Fatal("protecting / must be rejected")
	}
	if _, err := (Spec{}).Normalize(); err == nil {
		t.Fatal("empty spec must be rejected")
	}
}
