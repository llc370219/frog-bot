package bot

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCheckUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, current, want string
		code                int
	}{
		{"newer release", "v1.9.9", "available", 200},
		{"same release", "v1.10.0", "current", 200},
		{"current is newer", "v2.0.0", "current", 200},
		{"local prerelease", "v1.10.0-beta.1", "available", 200},
		{"local build", "开发版", "unversioned", 200},
		{"no published release", "v1.0.0", "no_release", 404},
		{"rate limited", "v1.0.0", "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.code, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1.10.0"}`))}, nil
			})}
			status, err := checkUpdate(context.Background(), client, latestReleaseAPI, tc.current)
			if tc.want == "" {
				if err == nil {
					t.Fatal("expected a check error, not an up-to-date result")
				}
			} else if err != nil || status.State != tc.want {
				t.Fatalf("checkUpdate = %+v, %v", status, err)
			}
		})
	}
}

func TestCheckUpdateBetaAndChannelSwitch(t *testing.T) {
	listPayload := `[
		{"tag_name":"clients-v0.2.0-beta.1","draft":false},
		{"tag_name":"v1.11.0-beta","draft":false},
		{"tag_name":"v1.10.0","draft":false}
	]`
	client := &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(listPayload))}, nil
	})}

	// 稳定版切到 beta 通道应检测到 v1.11.0-beta 并返回完整可选版本列表
	st, err := checkUpdateForChannel(context.Background(), client, listReleasesAPI, "v1.10.0", "beta")
	if err != nil {
		t.Fatal(err)
	}
	if st.Latest != "v1.11.0-beta" || st.State != "available" || st.Channel != "beta" || len(st.Releases) != 2 {
		t.Fatalf("unexpected beta status: %+v", st)
	}

	// stable 通道只筛正式版列表
	stStable, err := checkUpdateForChannel(context.Background(), client, listReleasesAPI, "v1.9.0", "stable")
	if err != nil {
		t.Fatal(err)
	}
	if stStable.Latest != "v1.10.0" || len(stStable.Releases) != 1 || stStable.Releases[0].Tag != "v1.10.0" {
		t.Fatalf("unexpected stable status: %+v", stStable)
	}

	// 已在最新 beta 上时显示 current
	stSame, err := checkUpdateForChannel(context.Background(), client, listReleasesAPI, "v1.11.0-beta", "beta")
	if err != nil {
		t.Fatal(err)
	}
	if stSame.State != "current" {
		t.Fatalf("expected current on same beta, got %+v", stSame)
	}

	// 从 beta 切回 stable 通道允许切回 v1.10.0
	if got := compareChannelState("v1.11.0-beta", "v1.10.0", "stable"); got != "available" {
		t.Fatalf("expected available when switching from beta back to stable, got %q", got)
	}
}
