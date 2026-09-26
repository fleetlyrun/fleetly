package imageregistry

import (
	"strings"
	"testing"
)

// TestParseForms 解析形态矩阵：显式 host / Docker Hub 归一与 library 补齐 /
// tag 与 digest / localhost 与端口 / 缺省 tag=latest。
func TestParseForms(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		name string
		in   string
		want Reference
	}{
		{
			name: "explicit host with tag",
			in:   "ghcr.io/owner/app:1.2.3",
			want: Reference{Host: "ghcr.io", Repository: "owner/app", Tag: "1.2.3"},
		},
		{
			name: "explicit host with digest",
			in:   "ghcr.io/owner/app@" + digest,
			want: Reference{Host: "ghcr.io", Repository: "owner/app", Digest: digest},
		},
		{
			name: "tag and digest pinned form keeps both",
			in:   "ghcr.io/owner/app:1.2.3@" + digest,
			want: Reference{Host: "ghcr.io", Repository: "owner/app", Tag: "1.2.3", Digest: digest},
		},
		{
			name: "docker hub official image gains the library prefix",
			in:   "alpine:3.19",
			want: Reference{Host: DockerHubHost, Repository: "library/alpine", Tag: "3.19"},
		},
		{
			name: "docker hub namespaced image defaults to latest",
			in:   "someuser/app",
			want: Reference{Host: DockerHubHost, Repository: "someuser/app", Tag: "latest"},
		},
		{
			name: "docker.io explicit normalizes to the v2 endpoint",
			in:   "docker.io/alpine",
			want: Reference{Host: DockerHubHost, Repository: "library/alpine", Tag: "latest"},
		},
		{
			name: "docker.io namespaced keeps the namespace",
			in:   "docker.io/someuser/app:edge",
			want: Reference{Host: DockerHubHost, Repository: "someuser/app", Tag: "edge"},
		},
		{
			name: "localhost registry with port",
			in:   "localhost:5000/team/app:v1",
			want: Reference{Host: "localhost:5000", Repository: "team/app", Tag: "v1"},
		},
		{
			name: "registry host with port",
			in:   "registry.example.test:5000/a/b",
			want: Reference{Host: "registry.example.test:5000", Repository: "a/b", Tag: "latest"},
		},
		{
			name: "explicit host without tag uses latest",
			in:   "ghcr.io/owner/app",
			want: Reference{Host: "ghcr.io", Repository: "owner/app", Tag: "latest"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("Parse(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseRejectsInvalidForms 形态非法点名拒绝（调用点按解析腿失败回落
// 本机 inspect，不阻断部署）。
func TestParseRejectsInvalidForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "scheme", in: "https://ghcr.io/owner/app:1"},
		{name: "uppercase repository", in: "ghcr.io/Owner/App:1"},
		{name: "tag with space", in: "ghcr.io/owner/app:bad tag"},
		{name: "digest with invalid hex", in: "ghcr.io/owner/app@sha256:zzzz"},
		{name: "empty digest", in: "ghcr.io/owner/app@"},
		{name: "trailing slash without repository", in: "ghcr.io/owner/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.in); err == nil {
				t.Fatalf("Parse(%q) = nil error, want explicit rejection", tc.in)
			}
		})
	}
}

// TestNormalizeHost 归一形态矩阵（设置面与服务端匹配共用同一函数——
// 单一事实源）。
func TestNormalizeHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ghcr.io", "ghcr.io"},
		{"GHCR.IO", "ghcr.io"},
		{" https://ghcr.io/ ", "ghcr.io"},
		{"http://registry.example.test:5000", "registry.example.test:5000"},
		{"docker.io", DockerHubHost},
		{"index.docker.io", DockerHubHost},
		{"registry.hub.docker.com", DockerHubHost},
		{"", ""},
	}
	for _, tc := range cases {
		if got := NormalizeHost(tc.in); got != tc.want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
