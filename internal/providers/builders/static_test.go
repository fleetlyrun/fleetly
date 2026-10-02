package builders

// static Provider 单测（ADR-0032 验收锚）：生成 Dockerfile/Caddyfile 黄金
// 文本（heredoc 形态、caddy 钉版 tag、output_dir 内插）、output_dir 归并
// 复验。Solve 面走共享 daemon 机械（集成测试 env-gated 同 dockerfile）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

func TestStaticDockerfileGolden(t *testing.T) {
	assert.Equal(t, `FROM caddy:2.11-alpine
COPY <<'EOF' /etc/caddy/Caddyfile
:8080 {
	root * /srv
	try_files {path} /index.html
	file_server
}
EOF
COPY dist/ /srv/
`, staticDockerfile("dist"), "generated Dockerfile is the pinned wrapper (heredoc Caddyfile first for cache)")
}

func TestCleanStaticOutputDir(t *testing.T) {
	cases := map[string]string{
		"":        ".",    // 归一化面兜底
		".":       ".",    // 上下文根
		"dist":    "dist", // 常见产物目录
		"./dist":  "dist", // 前缀 ./ 归并
		"a//b":    "a/b",  // 重复斜杠归并
		"a/./b":   "a/b",
		"a/../b":  "b", // 回上一级但仍在界内
		"public/": "public",
	}
	for in, want := range cases {
		got, err := cleanStaticOutputDir(in)
		require.NoError(t, err, "input %q", in)
		assert.Equal(t, want, got, "input %q", in)
	}
	for _, bad := range []string{"..", "../x", "a/../../x", "/abs", "C:\\x", "a\\b"} {
		_, err := cleanStaticOutputDir(bad)
		assert.Error(t, err, "input %q must be rejected (context escape)", bad)
	}
}

func TestStaticBuildRejectsMismatchedPayload(t *testing.T) {
	p, err := NewStatic(context.Background(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	_, err = p.Build(context.Background(), capability.BuildRequest{BuildID: "b1", Builder: "static"}, &logCollector{&[]string{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no static strategy payload")

	// Describe Notes 携带钉版镜像与"无构建步"边界（能力自描述面）。
	notes := p.Describe().Notes
	assert.Contains(t, notes[0], staticServerImage)
	assert.Contains(t, notes[0], "does not run a build step")
}
