package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFailedVaultMutationIsFullyRolledBack(t *testing.T) {
	for _, op := range []string{"update", "insert", "delete", "values"} {
		t.Run(op, func(t *testing.T) {
			dir := t.TempDir()
			key, _, err := LoadKey("", dir)
			if err != nil {
				t.Fatal(err)
			}
			v, err := Open(dir, key)
			if err != nil {
				t.Fatal(err)
			}
			if err := v.Set("TEST_TOKEN", "old-private-value", "old description", &Scope{Tools: []string{"shell"}}); err != nil {
				t.Fatal(err)
			}
			infos := v.List()
			original := v.path
			v.path = filepath.Join(t.TempDir(), "blocked")
			if err := os.Mkdir(v.path, 0700); err != nil {
				t.Fatal(err)
			}
			switch op {
			case "update":
				err = v.Set("TEST_TOKEN", "new-private-value", "new description", &Scope{Tools: []string{"read_file"}})
			case "insert":
				err = v.Set("NEW_TOKEN", "new-private-value", "", nil)
			case "delete":
				err = v.Delete("TEST_TOKEN")
			case "values":
				_, err = v.Values([]string{"TEST_TOKEN", "TEST_TOKEN"})
			}
			v.path = original
			if err == nil {
				t.Fatal("expected injected rename failure")
			}
			if !reflect.DeepEqual(v.List(), infos) || !v.Has("TEST_TOKEN") || v.Has("NEW_TOKEN") {
				t.Fatal("failed mutation changed memory")
			}
			if strings.Contains(v.Redact("old-private-value"), "old-private-value") {
				t.Fatal("redaction lost after failed mutation")
			}
			if err := v.Set("OTHER_TOKEN", "other-private-value", "", nil); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(dir, key)
			if err != nil {
				t.Fatal(err)
			}
			values, err := reopened.Values([]string{"TEST_TOKEN"})
			if err != nil {
				t.Fatal(err)
			}
			if values["TEST_TOKEN"] != "old-private-value" {
				t.Fatal("later save committed failed ciphertext update")
			}
			if !reflect.DeepEqual(v.List()[1], infos[0]) {
				t.Fatal("failed metadata update was committed")
			}
		})
	}
}

func TestVaultSaveReplacesFileAtomically(t *testing.T) {
	dir := t.TempDir()
	key, _, err := LoadKey("", dir)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Open(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set("TEST_TOKEN", "first-value", "", nil); err != nil {
		t.Fatal(err)
	}
	backup := v.path + ".snapshot"
	if err := os.Link(v.path, backup); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set("TEST_TOKEN", "second-value", "", nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("save overwrote the old inode")
	}
	info, err := os.Stat(v.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("secret file permissions: %v", info.Mode())
	}
}
