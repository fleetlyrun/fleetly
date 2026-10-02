package builders

// railpack Provider 单测（ADR-0032 验收锚）：版本探测解析、钉版执法
// （spec pin 与二进制双侧）、frontend ref 组装、prepare 命令构造与退出码
// 语义映射。全部走 runCmd 接缝假底座，零外部依赖。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// withFakeCmd 替换 runCmd 接缝（测试期生效，收尾恢复）。
func withFakeCmd(t *testing.T, fn func(ctx context.Context, name string, args []string) cmdOutcome) {
	t.Helper()
	prev := runCmd
	runCmd = func(ctx context.Context, name string, args ...string) cmdOutcome {
		return fn(ctx, name, args)
	}
	t.Cleanup(func() { runCmd = prev })
}

func TestRailpackVersionParse(t *testing.T) {
	cases := map[string]string{
		"railpack 0.39.0\n": "0.39.0", // clap 默认形态
		"0.39.0\n":          "0.39.0", // 裸 semver 形态
		"v0.39.0":           "0.39.0", // v 前缀容错
		"railpack 1.2.3":    "1.2.3",
		"":                  "", // 空输出
	}
	for out, want := range cases {
		withFakeCmd(t, func(context.Context, string, []string) cmdOutcome {
			return cmdOutcome{Combined: []byte(out)}
		})
		assert.Equal(t, want, probeRailpackVersion(context.Background(), "railpack"), "input %q", out)
	}
	withFakeCmd(t, func(context.Context, string, []string) cmdOutcome {
		return cmdOutcome{Err: errors.New("not found"), ExitCode: -1}
	})
	assert.Empty(t, probeRailpackVersion(context.Background(), "railpack"), "missing binary must probe to empty")
}

func TestRailpackFrontendRef(t *testing.T) {
	assert.Equal(t, "ghcr.io/railwayapp/railpack-frontend:v0.39.0", railpackFrontendRef("0.39.0"),
		"frontend image tag carries the v prefix (GHCR tag form)")
	assert.Equal(t, "0.39.0", railpackPinnedVersion, "platform pin is the archived-verified baseline (ADR-0021)")
}

// newRailpackForTest 构造版本可控的 Provider（daemon 客户端惰性连接，
// 版本检查先于任何 daemon 调用——本测试面零 daemon 依赖）。
func newRailpackForTest(t *testing.T, version string) *RailpackProvider {
	t.Helper()
	withFakeCmd(t, func(context.Context, string, []string) cmdOutcome {
		return cmdOutcome{Combined: []byte("railpack " + version + "\n")}
	})
	p, err := NewRailpack(context.Background(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestRailpackBuildEnforcesSpecPin(t *testing.T) {
	p := newRailpackForTest(t, railpackPinnedVersion)
	_, err := p.Build(context.Background(), capability.BuildRequest{
		BuildID: "b1", Builder: "railpack",
		Railpack: &capability.RailpackInput{PinnedVersion: "0.38.0"},
	}, &logCollector{&[]string{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), railpackPinnedVersion, "the error must name the platform's pinned version")
	assert.Contains(t, err.Error(), "--railpack-version", "the error must show the CLI flag form")

	// 非 railpack 请求路由失配（防御纵深）。
	_, err = p.Build(context.Background(), capability.BuildRequest{BuildID: "b2", Builder: "railpack"}, &logCollector{&[]string{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no railpack strategy payload")
}

func TestRailpackBuildEnforcesBinaryPin(t *testing.T) {
	p := newRailpackForTest(t, "0.40.1") // 安装了非钉版二进制
	_, err := p.Build(context.Background(), capability.BuildRequest{
		BuildID: "b1", Builder: "railpack",
		Railpack: &capability.RailpackInput{PinnedVersion: railpackPinnedVersion},
	}, &logCollector{&[]string{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "0.40.1")
	assert.Contains(t, err.Error(), railpackPinnedVersion)
}

func TestRailpackHealthReportsBinaryState(t *testing.T) {
	// 二进制缺席：Health 不健康且文本带钉版与接缝名（daemon ping 在本机
	// 不可达的环境下同样不健康——断言聚焦二进制面文本）。
	withFakeCmd(t, func(context.Context, string, []string) cmdOutcome {
		return cmdOutcome{Err: errors.New("not found"), ExitCode: -1}
	})
	p, err := NewRailpack(context.Background(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	h := p.Health(context.Background())
	require.False(t, h.Healthy)
	assert.Contains(t, h.Details, railpackPinnedVersion)
	assert.Contains(t, h.Details, "FLEETLY_RAILPACK_BIN")

	// Describe Notes 携带钉版（能力自描述面的版本可发现处）。
	assert.Contains(t, strings.Join(p.Describe().Notes, "; "), railpackPinnedVersion)
}

func TestRailpackPrepareExitSemantics(t *testing.T) {
	ctx := context.Background()
	req := capability.BuildRequest{BuildID: "b9", Builder: "railpack", ContextDir: t.TempDir()}
	planDir := t.TempDir()
	w := &logCollector{&[]string{}}

	t.Run("success writes the plan", func(t *testing.T) {
		withFakeCmd(t, func(_ context.Context, _ string, args []string) cmdOutcome {
			// 假底座按 --plan-out 落计划文件（真机 railpack 行为）。
			for i, a := range args {
				if a == "--plan-out" && i+1 < len(args) {
					require.NoError(t, os.WriteFile(args[i+1], []byte("{}"), 0o600))
				}
			}
			return cmdOutcome{Combined: []byte("detected node provider\n")}
		})
		require.NoError(t, runRailpackPrepare(ctx, "railpack", req, planDir, w))
		require.FileExists(t, filepath.Join(planDir, "railpack-plan.json"))
	})

	t.Run("exit 75 is transient", func(t *testing.T) {
		withFakeCmd(t, func(context.Context, string, []string) cmdOutcome {
			return cmdOutcome{Err: errors.New("exit 75"), ExitCode: railpackPrepareExitTransient, Combined: []byte("fetch failed")}
		})
		err := runRailpackPrepare(ctx, "railpack", req, planDir, w)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "transiently")
		assert.Contains(t, err.Error(), "retry the deploy")
	})

	t.Run("exit 1 is permanent", func(t *testing.T) {
		withFakeCmd(t, func(context.Context, string, []string) cmdOutcome {
			return cmdOutcome{Err: errors.New("exit 1"), ExitCode: 1, Combined: []byte("no provider detected")}
		})
		err := runRailpackPrepare(ctx, "railpack", req, planDir, w)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rejected the source")
	})

	t.Run("success without a plan file fails honestly", func(t *testing.T) {
		withFakeCmd(t, func(context.Context, string, []string) cmdOutcome {
			return cmdOutcome{Combined: []byte("ok")}
		})
		err := runRailpackPrepare(ctx, "railpack", req, t.TempDir(), w)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "wrote no plan file")
	})

	t.Run("prepare output streams into the build log", func(t *testing.T) {
		lines := &[]string{}
		withFakeCmd(t, func(_ context.Context, _ string, args []string) cmdOutcome {
			for i, a := range args {
				if a == "--plan-out" && i+1 < len(args) {
					require.NoError(t, os.WriteFile(args[i+1], []byte("{}"), 0o600))
				}
			}
			return cmdOutcome{Combined: []byte("detected go provider")}
		})
		require.NoError(t, runRailpackPrepare(ctx, "railpack", req, t.TempDir(), &logCollector{lines}))
		assert.Contains(t, strings.Join(*lines, "\n"), "detected go provider")
	})
}
