package build

// Builder 是构建执行器（Executor 接口的实现）：一次构建的端到端编排——
// 请求解码 → 产物目录/日志 → 驱动分派（railpack LLB / dockerfile 前端）→
// buildkit solve + 本机装载 → inspect 取不可变 digest → builds 状态落库
// （终态迁移 + 审计同事务在 state 层完成）。失败归一为 E_BUILD_FAILED
// 信封（stderr 尾部与日志路径进 context——T2.8「失败带 stderr 与建议」）。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// daemonEnsureInterval 是 buildkitd 就绪收敛的重试间隔。
const daemonEnsureInterval = 15 * time.Second

// Executor 是构建队列消费的执行契约（单测注入假执行器测并发上限）。
type Executor interface {
	// Execute 执行一条已 claim（building）的构建记录，终态落库后返回最新
	// 记录；返回的错误已归一为应用错误信封。
	Execute(ctx context.Context, rec state.BuildRecord) (state.BuildRecord, error)
}

// Builder 依赖：配置 + 状态库 + 本机镜像端口 + buildkitd 编排端口 + solve
// 执行器。
type Builder struct {
	cfg    Config
	store  *state.Store
	images ImageSource
	daemon DaemonManager
	solver *solveRunner
	log    *slog.Logger
}

// NewBuilder 构建构建执行器。daemon 可为 nil（装配测试干跑；自管容器形态
// 下执行前的就绪收敛由 ensureDaemonReady 承载）。
func NewBuilder(cfg Config, store *state.Store, images ImageSource, daemon DaemonManager, log *slog.Logger) *Builder {
	cfg = cfg.Normalize()
	return &Builder{
		cfg:    cfg,
		store:  store,
		images: images,
		daemon: daemon,
		solver: newSolveRunner(cfg.BuildkitHost, images),
		log:    log,
	}
}

// Config 返回归一化后的配置（服务装配与诊断用）。
func (b *Builder) Config() Config { return b.cfg }

// CacheLocalPath 返回 buildkit local cache 的宿主数据根（客户端侧路径）。
func (b *Builder) CacheLocalPath() string { return b.cfg.CacheDir }

// DaemonSpec 返回平台自管 buildkitd 容器的目标形态（ManageDaemon=false 时
// 返回 ok=false——外部端点形态不自管容器）。
func (b *Builder) DaemonSpec() (DaemonSpec, bool) {
	if !b.cfg.ManageDaemon {
		return DaemonSpec{}, false
	}
	return DaemonSpec{
		Name:        b.cfg.DaemonContainerName,
		Image:       BuildkitImage,
		MemoryBytes: b.cfg.MemoryBytes,
		NanoCPUs:    b.cfg.NanoCPUs,
		CacheVolume: b.cfg.CacheVolume,
	}, true
}

// executeBudget 是执行前 buildkitd 就绪收敛的重试预算（首建等钉版镜像
// 拉取——冷拉取受网络主导，Spike A 实测量级分钟；6 分钟覆盖慢网）。
const executeEnsureBudget = 6 * time.Minute

// ensureDaemonReady 同步收敛平台自管 buildkitd（ManageDaemon=false 或
// daemon 端口为空 = no-op）。带重试预算的幂等收敛：镜像拉取中/容器重启
// 都在此等待而非终态失败——构建因基础设施暂未就绪而终态失败是把可重试
// 错误当永久错误（实测缺陷：warm-up 后台化后首建抢跑）。
func (b *Builder) ensureDaemonReady(ctx context.Context) error {
	spec, ok := b.DaemonSpec()
	if !ok || b.daemon == nil {
		return nil
	}
	deadline := time.Now().Add(executeEnsureBudget)
	var lastErr error
	for {
		if spec.CacheVolume != "" {
			if err := b.daemon.EnsureVolumePresent(ctx, spec.CacheVolume); err != nil {
				lastErr = err
			}
		}
		if lastErr == nil {
			if err := b.daemon.EnsureContainerRunning(ctx, spec); err != nil {
				lastErr = err
			}
		}
		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return lastErr
		}
		if !time.Now().Before(deadline) {
			return fmtErr("buildkitd not ready within %s: %w", executeEnsureBudget, lastErr)
		}
		b.log.Warn("buildkitd not ready, retrying", "container", spec.Name, "error", lastErr)
		select {
		case <-ctx.Done():
			return lastErr
		case <-time.After(daemonEnsureInterval):
		}
	}
}

// EnsureDaemonReady 是 ensureDaemonReady 的公开形态（服务壳预热路径用；
// 幂等，可在构建执行前任意次调用）。
func (b *Builder) EnsureDaemonReady(ctx context.Context) error { return b.ensureDaemonReady(ctx) }

// Execute 实现执行器契约。
func (b *Builder) Execute(ctx context.Context, rec state.BuildRecord) (state.BuildRecord, error) {
	if rec.Status != state.BuildBuilding {
		return rec, fmtErr("build %s must be claimed (building) before execute, got %s", rec.ID, rec.Status)
	}
	// 终态读写用去取消化 ctx：执行 ctx 是队列服务生存期 ctx，优雅关停的
	// 取消可能落在 solve 完成后/失败处理中——终态落库不得随之丢失（否则
	// builds 行永久停留 building）。WithoutCancel 保留 trace/log 上下文、
	// 不继承取消与 deadline，正合「终态必达」语义；run/solve 本身仍用原
	// ctx（关停即取消，正确）。
	finCtx := context.WithoutCancel(ctx)
	if err := b.ensureDaemonReady(ctx); err != nil {
		return state.BuildRecord{}, b.fail(finCtx, rec.ID, err)
	}
	req, err := DecodeRequest(rec.Request)
	if err != nil {
		// 请求损坏是入队方错误：终态 failed（E_BUILD_FAILED，无日志可附）。
		return state.BuildRecord{}, b.fail(finCtx, rec.ID, err)
	}
	// H14 执行侧校验（纵深防御）：ContextDir 必须位于受管根内——
	// builds.request 是跨进程通道（CLI/API 入队之外的直写库形态同样可
	// 达），API 层包含性校验之外的最后一道门。越界即终态失败
	// （E_BUILD_FAILED，信息注明上下文目录越界），不静默放宽。
	if err := validateContextDir(req.ContextDir, b.cfg.ContextRoots); err != nil {
		return state.BuildRecord{}, b.fail(finCtx, rec.ID, err)
	}

	artDir := ArtifactsDir(b.cfg.ArtifactsDir, req.AppName, rec.ID)
	if err := os.MkdirAll(artDir, 0o750); err != nil {
		return state.BuildRecord{}, b.fail(finCtx, rec.ID, fmtErr("create artifacts dir %s: %w", artDir, err))
	}
	logPath := filepath.Join(artDir, "build.log")
	planPath := filepath.Join(artDir, "railpack-plan.json")

	imageRef, err := ImageRef(req.AppName, rec.ID)
	if err != nil {
		return state.BuildRecord{}, b.fail(finCtx, rec.ID, err)
	}

	digest, took, buildErr := b.run(ctx, rec, req, imageRef, planPath, logPath)
	if buildErr != nil {
		// 超时预算耗尽（队列以 WithTimeout 注入的 per-build deadline）：
		// 失败信息注明预算供定位（预算 ≈ deadline − 认领时间，claim 盖章）。
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && !rec.StartedAt.IsZero() {
			if dl, ok := ctx.Deadline(); ok && dl.After(rec.StartedAt) {
				buildErr = fmtErr("build timed out after ~%s budget (config build.timeout_seconds): %w",
					dl.Sub(rec.StartedAt).Round(time.Second), buildErr)
			}
		}
		return state.BuildRecord{}, b.fail(finCtx, rec.ID, buildErr)
	}

	if err := b.store.FinishBuildSucceeded(finCtx, rec.ID, imageRef, digest, planPathFor(rec, planPath), logPath); err != nil {
		return state.BuildRecord{}, fmtErr("record build %s succeeded: %w", rec.ID, err)
	}
	b.log.Info("build succeeded",
		"build", rec.ID, "app", req.AppName, "service", rec.Service,
		"driver", string(rec.Driver), "ref", imageRef, "digest", digest,
		"seconds", fmt.Sprintf("%.2f", took))
	updated, err := b.store.GetBuild(finCtx, rec.ID)
	if err != nil {
		return state.BuildRecord{}, fmtErr("read build %s: %w", rec.ID, err)
	}
	return updated, nil
}

// run 分派驱动执行构建，返回镜像 ID digest 与耗时。
func (b *Builder) run(ctx context.Context, rec state.BuildRecord, req Request, imageRef, planPath, logPath string) (string, float64, error) {
	started := time.Now()
	logf, err := os.Create(logPath) //nolint:gosec // G304：logPath 是平台配置产物目录下的受控派生路径（config build.artifacts_dir）
	if err != nil {
		return "", 0, fmtErr("create build log %s: %w", logPath, err)
	}
	defer func() { _ = logf.Close() }()

	switch rec.Driver {
	case state.DriverRailpack:
		def, imageConfig, planJSON, err := railpackPlan(ctx, req)
		if err != nil {
			return "", 0, err
		}
		if err := writePlanArchive(planPath, planJSON); err != nil {
			return "", 0, err
		}
		opts, err := railpackSolveOptions(req, imageRef, imageConfig, b.CacheLocalPath())
		if err != nil {
			return "", 0, err
		}
		if err := b.solver.solveLLB(ctx, opts, def, logf); err != nil {
			return "", 0, err
		}
	case state.DriverDockerfile:
		opts, err := dockerfileSolveOptions(req, imageRef, b.CacheLocalPath())
		if err != nil {
			return "", 0, err
		}
		if err := b.solver.solveFrontend(ctx, opts, logf); err != nil {
			return "", 0, err
		}
	default:
		return "", 0, fmtErr("unsupported build driver %q", rec.Driver)
	}

	info, err := b.images.InspectImage(ctx, imageRef)
	if err != nil {
		return "", 0, fmtErr("inspect built image %s: %w", imageRef, err)
	}
	return info.ID, time.Since(started).Seconds(), nil
}

// fail 归一失败终态：E_BUILD_FAILED 信封（stderr 尾部 + 日志路径进
// context）+ builds failed 落库。终态写一律去取消化（WithoutCancel）——
// 失败处理常发生在执行 ctx 已取消时（超时/关停），落库必达。
func (b *Builder) fail(ctx context.Context, buildID string, cause error) error {
	ctx = context.WithoutCancel(ctx)
	rec, err := b.store.GetBuild(ctx, buildID)
	if err == nil && rec.LogPath != "" {
		tail := tailFile(rec.LogPath)
		if tail != "" {
			cause = fmt.Errorf("%w\n--- 构建日志尾部 ---\n%s", cause, tail)
		}
	}
	appErr := apperr.New("E_BUILD_FAILED", "构建失败：%v", cause).
		WithPhase("build").
		WithCause(cause)
	if err == nil {
		if rec.LogPath != "" {
			appErr = appErr.WithContext("log_path", rec.LogPath)
		}
		if ferr := b.store.FinishBuildFailed(ctx, buildID, "E_BUILD_FAILED"); ferr != nil {
			b.log.Error("record build failure", "build", buildID, "error", ferr)
		}
	}
	b.log.Error("build failed", "build", buildID, "error", cause)
	return appErr
}

// planPathFor 仅 railpack 驱动回填 plan_path（dockerfile 无 plan 产物）。
func planPathFor(rec state.BuildRecord, planPath string) string {
	if rec.Driver == state.DriverRailpack {
		return planPath
	}
	return ""
}
