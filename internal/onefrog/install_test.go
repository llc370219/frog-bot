//go:build !windows

package onefrog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type tarEntry struct {
	name, body, link string
	mode             int64
}

func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.link != "" {
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, e.link, 0
		} else if strings.HasSuffix(e.name, "/") {
			h.Typeflag, h.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractTarGzRejectsEscapes(t *testing.T) {
	for _, bad := range [][]tarEntry{
		{{name: "node-v22/../../evil", body: "x", mode: 0o644}},
		{{name: "node-v22/bin/x", link: "/etc/passwd"}},
		{{name: "node-v22/bin/x", link: "../../../etc/passwd"}},
	} {
		if err := extractTarGz(bytes.NewReader(makeTarGz(t, bad)), t.TempDir()); err == nil {
			t.Fatalf("expected rejection for %+v", bad)
		}
	}
}

// 假的 Node.js 下载站 + 假的 npm：走一遍完整安装流程（下载、校验、解压、npm 安装、--version 检查）。
func TestInstallCLIEndToEnd(t *testing.T) {
	platform, err := nodePlatform()
	if err != nil {
		t.Skip(err)
	}
	const version = "v22.99.0"
	name := fmt.Sprintf("node-%s-%s.tar.gz", version, platform)
	pkg := makeTarGz(t, []tarEntry{
		{name: "node-" + version + "/bin/", mode: 0o755},
		{name: "node-" + version + "/bin/node", mode: 0o755, body: "#!/bin/sh\necho " + version + "\n"},
		// 真实的 npm 是指向 lib/node_modules/npm 的链接；这里的 npm 把一个假的 pi 写进 --prefix/bin。
		{name: "node-" + version + "/lib/npm-cli.sh", mode: 0o755, body: `#!/bin/sh
command -v node >/dev/null || { echo "node not on PATH" >&2; exit 1; }
while [ $# -gt 0 ]; do [ "$1" = --prefix ] && prefix=$2; last=$1; shift; done
[ "$last" = "@earendil-works/pi-coding-agent@latest" ] || { echo "bad package $last" >&2; exit 1; }
mkdir -p "$prefix/bin"
printf '#!/usr/bin/env node\n' > "$prefix/bin/pi"
chmod +x "$prefix/bin/pi"
echo "added 1 package"
`},
		{name: "node-" + version + "/bin/npm", link: "../lib/npm-cli.sh"},
	})
	sum := sha256.Sum256(pkg)
	var tarballHits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			file := strings.Replace(platform, "darwin", "osx", 1) + "-tar"
			fmt.Fprintf(w, `[{"version":"v24.1.0","files":[%q]},{"version":%q,"files":[%q]},{"version":"v22.1.0","files":[%q]}]`, file, version, file, file)
		case "/" + version + "/SHASUMS256.txt":
			fmt.Fprintf(w, "%s  %s\n%s  other.tar.gz\n", hex.EncodeToString(sum[:]), name, strings.Repeat("0", 64))
		case "/" + version + "/" + name:
			tarballHits++
			w.Write(pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := downloadClient
	downloadClient = srv.Client()
	defer func() { downloadClient = old }()
	t.Setenv("FROGBOT_NODE_MIRROR", srv.URL)
	home := fakeHome(t)
	// PATH 里已经有一个别的 pi：装好后应该用 frog 装的那个。
	other := filepath.Join(home, "other-bin")
	writeExe(t, filepath.Join(other, "pi"), "#!/bin/sh\necho other\n")
	t.Setenv("PATH", other+":/usr/bin:/bin")
	dir := filepath.Join(t.TempDir(), "tools")
	SetToolsDir(dir)
	defer SetToolsDir("")

	status := DetectCLIs()
	if status[2].ID != "pi" || status[2].Path != filepath.Join(other, "pi") || status[2].Managed || !status[2].Installable {
		t.Fatalf("before install: %+v", status[2])
	}
	if status[3].Installable {
		t.Fatalf("grok has no npm package and must not be installable: %+v", status[3])
	}
	waitInstall := func() *InstallState {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if j := installSnapshot("pi"); j != nil && j.State != "running" {
				return j
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("install did not finish")
		return nil
	}
	if err := StartInstall("pi"); err != nil {
		t.Fatal(err)
	}
	if j := waitInstall(); j.State != "done" || j.Version != version {
		t.Fatalf("install: %+v", j)
	}
	status = DetectCLIs()
	if p := status[2]; !p.Installed || !p.Managed || p.Path != filepath.Join(dir, "clis", "bin", "pi") {
		t.Fatalf("after install: %+v", p)
	}
	// 运行时要能找到 frog 装的 node（pi 是 #!/usr/bin/env node）。
	out, err := runCLI(context.Background(), "pi", t.TempDir(), "hi", CLIOptions{})
	if err != nil || strings.TrimSpace(out) != version {
		t.Fatalf("runCLI = %q, %v", out, err)
	}
	// 再装一次（更新）：Node 已经能用，不会重新下载。
	if err := StartInstall("pi"); err != nil {
		t.Fatal(err)
	}
	if j := waitInstall(); j.State != "done" || tarballHits != 1 {
		t.Fatalf("update: %+v, downloads %d", j, tarballHits)
	}
	if err := StartInstall("grok"); err == nil {
		t.Fatal("grok must not be installable")
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	platform, err := nodePlatform()
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/index.json":
			fmt.Fprintf(w, `[{"version":"v22.5.0","files":[%q]}]`, strings.Replace(platform, "darwin", "osx", 1)+"-tar")
		case strings.HasSuffix(r.URL.Path, "SHASUMS256.txt"):
			fmt.Fprintf(w, "%s  node-v22.5.0-%s.tar.gz\n", strings.Repeat("ab", 32), platform)
		default:
			w.Write([]byte("tampered"))
		}
	}))
	defer srv.Close()
	old := downloadClient
	downloadClient = srv.Client()
	defer func() { downloadClient = old }()
	t.Setenv("FROGBOT_NODE_MIRROR", srv.URL)
	dir := t.TempDir()
	err = installNode(context.Background(), dir, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("want checksum error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node")); !os.IsNotExist(err) {
		t.Fatal("tampered package must not be installed")
	}
}
