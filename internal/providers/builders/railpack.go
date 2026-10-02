package builders

// railpack Builder Provider（ADR-0032）：钉版宿主二进制 prepare 产 plan +
// 钉版 frontend 镜像经 daemon 内嵌 buildkit gateway.v0 执行（railpack
// 官方生产推荐链；CLI 自带 build 走 docker load 管道，官方自评不适合
// 生产吞吐，弃用）。产物走共享 solveAndPush 链（moby 导入 → ImagePush →
// manifest digest）。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// railpackPinnedVersion 是平台钉版常量（ADR-0032 决策 4：ADR-0021 归档
// 验证基线；升级走 Platform 升级序 ADR-0015，无 env 覆写——钉版可被环境
// 变量漂移等于没钉）。双侧执法：spec.pinned_version 与安装二进制都必须
// 等于此值；frontend 镜像 tag 由 spec pin 派生。
const railpackPinnedVersion = "0.39.0"

// railpackFrontendImage 是 buildkit frontend 镜像（tag 带 v 前缀，与 spec
// bare semver 拼 v）。
const railpackFrontendImage = "ghcr.io/railwayapp/railpack-frontend"

// railpackPrepareExitTransient 是 railpack 约定的瞬态失败退出码（重试
// 可恢复；官方生产指南）。
const railpackPrepareExitTransient = 75

// RailpackProvider 是 railpack Builder Provider。
type RailpackProvider struct {
	d *daemonClients
	// bin 是宿主二进制路径（FLEETLY_RAILPACK_BIN 或 PATH 上的 railpack）。
	bin string
	// version 是探测到的二进制版本（空 = 二进制缺席或版本不可解析）。
	version string
}

// 编译期契约断言。
var _ capability.Builder = (*RailpackProvider)(nil)

// cmdOutcome 是执行接缝的结果面（ExitCode 仅 Err 非 nil 时有意义；
// -1 = 未启动/信号形态）。
type cmdOutcome struct {
	Combined []byte
	Err      error
	ExitCode int
}

// runCmd 是执行接 seam（真机 exec.CommandContext + CombinedOutput；单测
// 注入假底座——退出码是 railpack 约定语义面，结构化承载避免测面反构
// *exec.ExitError）。
var runCmd = func(ctx context.Context, name string, args ...string) cmdOutcome {
	//nolint:gosec // 二进制路径来自平台 env（操作者信任域，同 cloneSource 冻结 ref 口径）；spec 面只有钉版常量可执法
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	oc := cmdOutcome{Combined: out, Err: err, ExitCode: -1}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		oc.ExitCode = ee.ExitCode()
	}
	return oc
}

// NewRailpack 构造 Provider：host 为 daemon 端点；二进制路径 env
// FLEETLY_RAILPACK_BIN（缺省 PATH 上的 railpack）。构造期探测二进制版本
// （缺席/不匹配不阻断装配——Health 面 + Build 期精确失败，其余 builder
// 不受影响，ADR-0032 决策 4）。
func NewRailpack(ctx context.Context, host string) (*RailpackProvider, error) {
	d, err := newDaemonClients(ctx, host)
	if err != nil {
		return nil, err
	}
	bin := os.Getenv("FLEETLY_RAILPACK_BIN")
	if bin == "" {
		bin = "railpack"
	}
	return &RailpackProvider{d: d, bin: bin, version: probeRailpackVersion(ctx, bin)}, nil
}

// Close 释放底层连接。
func (p *RailpackProvider) Close() error { return p.d.Close() }

// Describe 实现 Provider 契约三件套之一（Notes 带平台钉版——能力自描述
// 面的版本可发现处）。
func (p *RailpackProvider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{
		Name:       "railpack",
		Capability: capability.KindBuilder,
		Version:    "1",
		Notes: []string{
			"zero-config source builds via pinned railpack " + railpackPinnedVersion + " (the spec must pin the same version; plan drift is why pinning is enforced)",
			"the railpack binary must be installed on the control-plane node (install.sh pins it; FLEETLY_RAILPACK_BIN overrides the path)",
			"builds execute as a pinned BuildKit frontend on the local daemon (ADR-0032); secrets and multi-arch land with later batches",
		},
	}
}

// Health 实现 Provider 契约三件套之一：daemon 可达 + 二进制在场且版本
// 匹配平台钉版。
func (p *RailpackProvider) Health(ctx context.Context) capability.HealthReport {
	if err := p.d.ping(ctx); err != nil {
		return capability.HealthReport{Healthy: false, Details: "docker daemon unreachable: " + err.Error()}
	}
	if p.version == "" {
		return capability.HealthReport{Healthy: false,
			Details: fmt.Sprintf("railpack binary %s is not runnable; install railpack %s (install.sh pins it) or set FLEETLY_RAILPACK_BIN", p.bin, railpackPinnedVersion)}
	}
	if p.version != railpackPinnedVersion {
		return capability.HealthReport{Healthy: false,
			Details: fmt.Sprintf("railpack binary reports %s but the platform pins %s; install the pinned version (ADR-0032)", p.version, railpackPinnedVersion)}
	}
	return capability.HealthReport{Healthy: true, Details: "railpack " + p.version + " + local daemon buildkit reachable"}
}

// Build 执行一次 railpack 构建：版本执法 → prepare（plan 落 per-build
// 临时目录，stdout/stderr 进构建日志）→ gateway.v0 Solve → 共享推送链。
func (p *RailpackProvider) Build(ctx context.Context, req capability.BuildRequest, w capability.LogWriter) (capability.BuildResult, error) {
	if req.Railpack == nil {
		return capability.BuildResult{}, fmt.Errorf("railpack: request carries no railpack strategy payload (routing mismatch)")
	}
	pin := req.Railpack.PinnedVersion
	if pin != railpackPinnedVersion {
		return capability.BuildResult{}, fmt.Errorf(
			"railpack: pinned version mismatch: the spec pins %q but this platform runs railpack %s; redeploy with --railpack-version %s",
			pin, railpackPinnedVersion, railpackPinnedVersion)
	}
	if p.version == "" {
		return capability.BuildResult{}, fmt.Errorf(
			"railpack: binary %s is not runnable; install railpack %s on the control-plane node (install.sh pins it) or set FLEETLY_RAILPACK_BIN",
			p.bin, railpackPinnedVersion)
	}
	if p.version != railpackPinnedVersion {
		return capability.BuildResult{}, fmt.Errorf(
			"railpack: binary reports %s but the platform pins %s; the installed binary must match (ADR-0032)", p.version, railpackPinnedVersion)
	}
	if req.ContextDir == "" {
		return capability.BuildResult{}, fmt.Errorf("railpack: build context directory is required")
	}
	if w == nil {
		return capability.BuildResult{}, fmt.Errorf("railpack: a log writer is required")
	}

	planDir, err := os.MkdirTemp("", "fleetly-railpack-"+req.BuildID+"-")
	if err != nil {
		return capability.BuildResult{}, fmt.Errorf("railpack: plan dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(planDir) }() //nolint:errcheck // 临时目录清理失败不致命

	if err := runRailpackPrepare(ctx, p.bin, req, planDir, w); err != nil {
		return capability.BuildResult{}, err
	}

	// gateway.v0 前端：source=钉版 frontend 镜像；plan 走 dockerfile mount
	//（与 buildx BUILDKIT_SYNTAX/-f plan.json 同语义）；cache-key=推送目标
	// repo 前缀（per-app mount 缓存隔离，跨 revision 稳定——BuildID 与
	// r<seq> tag 都不稳定）。
	attrs := map[string]string{
		"source":              railpackFrontendRef(pin),
		"filename":            "railpack-plan.json",
		"build-arg:cache-key": targetRepoPrefix(req.Target),
	}
	digest, err := p.d.solveAndPush(ctx, req, solveRequest{
		ContextDir:    req.ContextDir,
		DockerfileDir: planDir,
		Filename:      "railpack-plan.json",
		Frontend:      "gateway.v0",
		FrontendAttrs: attrs,
	}, w)
	if err != nil {
		return capability.BuildResult{}, err
	}
	return capability.BuildResult{Digest: digest}, nil
}

// runRailpackPrepare 执行 railpack prepare 并流式转发输出；退出码语义：
// 1=永久失败（输出即诊断）、75=瞬态（建议重试）、其他=通用失败。
func runRailpackPrepare(ctx context.Context, bin string, req capability.BuildRequest, planDir string, w capability.LogWriter) error {
	planOut := filepath.Join(planDir, "railpack-plan.json")
	out := runCmd(ctx, bin, "prepare", req.ContextDir, "--plan-out", planOut)
	if len(out.Combined) > 0 {
		_ = w.WriteLog(ctx, capability.LogFrame{WorkloadID: req.BuildID, Container: "railpack prepare", Line: out.Combined})
	}
	if out.Err == nil {
		if _, serr := os.Stat(planOut); serr != nil {
			return fmt.Errorf("railpack: prepare reported success but wrote no plan file: %v", serr)
		}
		return nil
	}
	switch out.ExitCode {
	case railpackPrepareExitTransient:
		return fmt.Errorf("railpack: prepare failed transiently (exit %d, e.g. upstream fetch); retry the deploy", railpackPrepareExitTransient)
	case 1:
		return fmt.Errorf("railpack: prepare rejected the source (no provider detected or configuration error); see the railpack prepare log above")
	}
	return fmt.Errorf("railpack: prepare: %w", out.Err)
}

// railpackFrontendRef 组装钉版 frontend 镜像引用（:v<pin>——frontend 镜像
// tag 与 CLI 版本同源同形）。
func railpackFrontendRef(pin string) string {
	return railpackFrontendImage + ":v" + pin
}

// probeRailpackVersion 探测二进制版本（`railpack --version` 输出末 token
// 去 v 前缀——输出形态 "railpack 0.39.0"/"0.39.0" 皆容；缺席/不可解析
// 返回空，Build/Health 面给出精确失败）。
func probeRailpackVersion(ctx context.Context, bin string) string {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out := runCmd(probeCtx, bin, "--version")
	if out.Err != nil {
		return ""
	}
	fields := strings.Fields(string(out.Combined))
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(fields[len(fields)-1], "v")
}

// init 自注册工厂（cmd/fleetlyd blank import 触发）。
func init() {
	capability.RegisterFactory(capability.KindBuilder, "railpack", func(ctx context.Context) (capability.Provider, error) {
		return NewRailpack(ctx, os.Getenv("DOCKER_HOST"))
	})
}
