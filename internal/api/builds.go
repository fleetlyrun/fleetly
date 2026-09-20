package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/build"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// BuildsService 实现 server.v1.BuildsService（T2.18）。TriggerBuild 与
// Deploy 同型契约：compose 内容字节入队前受控子集校验（compose 违约不动
// 底座、不入队），构建目标裁决（railpack/dockerfile + image 直通）在服务
// 端——旧 CLI 直连入库时在客户端做的 selectBuildTargets 随 CLI-over-SDK
// 改造收编到服务端，目标裁决单一事实源随行。builds.request 跨进程执行
// 契约不变（daemon 的 build.queue worker 消费同一 JSON 形态）。
//
// S18-A11：服务持 build.Queue，入队走 Queue.Enqueue（建行 + 唤醒）——
// 同进程触发立即唤醒扫描（不等 poll interval），Queue.Enqueue 自死代码
// 转正；queue 为 nil（进程内夹具形态）时回落直连建行，行为与旧路径一致。
type BuildsService struct {
	serverv1.UnimplementedBuildsServiceServer
	st    *state.Store
	queue *build.Queue
}

// NewBuildsService 构造 BuildsService（queue 可 nil——未接队列面的进程内
// 夹具形态，入队回落直连建行）。
func NewBuildsService(st *state.Store, queue *build.Queue) *BuildsService {
	return &BuildsService{st: st, queue: queue}
}

// TriggerBuild 解析 compose、裁决构建目标并逐服务入队（应用不存在时自动
// 创建——构建常是应用的第一个平台动作，与 deploy 同语义）。
func (s *BuildsService) TriggerBuild(ctx context.Context, req *serverv1.TriggerBuildRequest) (*serverv1.TriggerBuildResponse, error) {
	// compose 内容落临时文件（受控子集校验同 Deploy：临时文件生命周期 =
	// 本次解析，构建执行消费的是 request 里的 context 目录而非该文件）。
	// tmpdir: owned-by build worker（终态后回收，见 janitor/构建归档）——
	// MG-6：base_dir 空回落本目录时，context 目录需存活到异步 worker 执行，
	// 不能随请求 RemoveAll；回收归构建终态清理轨。
	dir, err := os.MkdirTemp("", "fleetly-compose-")
	if err != nil {
		return nil, fmt.Errorf("create compose temp dir: %w", err)
	}
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, req.GetCompose(), 0o600); err != nil { //nolint:gosec // G306：compose 内容非密钥，0600 保守
		return nil, fmt.Errorf("write compose temp file: %w", err)
	}
	spec, warnings, err := compose.Load(ctx, path)
	if err != nil {
		return nil, err // apperr（E_COMPOSE_*）原样透传
	}
	// A1（S18）：TriggerBuildRequest 无 app 字段（REST 面 POST /v1/builds
	// 也不含路径段），应用名单源 = compose name——不存在「请求 app 与
	// compose 应用名错位」的面（Deploy 侧的 A1 守卫在此无对应物）；若
	// 后续版本给请求加 app 字段，必须与 Deploy 同款一致性校验。

	// 上下文解析基准：显式 base_dir（单机同宿主语义）回落临时目录。
	// 基准本身先解析为绝对归一形态（H14：显式 base_dir 是调用方输入，
	// 包含性校验需要确定的锚点；临时回落形态天然绝对）。
	base := req.GetBaseDir()
	if base == "" {
		base = dir
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return nil, fmt.Errorf("resolve base_dir %s: %w", base, err)
	}

	app, err := ensureApp(ctx, s.st, spec.Name)
	if err != nil {
		return nil, err
	}

	out := &serverv1.TriggerBuildResponse{App: app.Name, Builds: []*serverv1.BuildView{}}
	for _, svc := range spec.Services {
		if filter := req.GetService(); filter != "" && svc.Name != filter {
			continue
		}
		driver, buildable, err := build.DriverFor(svc)
		if err != nil {
			return nil, err
		}
		if !buildable {
			out.Passthrough = append(out.Passthrough, &serverv1.PassthroughService{
				Service: svc.Name, Image: svc.Image,
			})
			continue
		}
		contextDir, err := resolveBuildContext(base, svc)
		if err != nil {
			return nil, err
		}
		// 构建 ID 在入队侧分配：request JSON 与 builds 行共用同一 ID（一次
		// 建行原子落库；DecodeRequest 校验 build_id 非空——旧 CLI 入队同
		// 一形态，T2.18 实机扫描发现的缺 ID 缺陷在此修正）。
		id := ulid.Make().String()
		raw, err := build.Request{
			BuildID:    id,
			AppID:      app.ID,
			AppName:    spec.Name,
			Service:    svc.Name,
			Driver:     driver,
			ContextDir: contextDir,
			Dockerfile: svc.Build.Dockerfile,
			SpecHash:   spec.SpecHash,
		}.Encode()
		if err != nil {
			return nil, err
		}
		// A11：入队走 Queue.Enqueue（建行 + 唤醒，单一写点——同进程触发
		// 不等 poll interval 即被认领）；无队列面（进程内夹具）回落直连
		// 建行，行为与旧路径一致。M4-8：入队审计带调用方归因（caller
		// token + actor=human，与 Deploy 的 auditEntry 模式对齐——H14
		// 敏感写面上行为人必须可追溯，不再恒 actor=system）。
		audit := state.AuditEntry{
			Actor:        "human",
			ActorTokenID: callerTokenID(ctx),
			Action:       "build.create",
			Target:       "app:" + app.ID,
			Result:       "ok",
			DiffSummary:  state.DiffSummary("build", id, "service", svc.Name, "driver", string(driver)), // B4：构造器替换手拼 JSON
		}
		if s.queue != nil {
			rec, err := s.queue.Enqueue(ctx, state.BuildRecord{
				ID:      id,
				AppID:   app.ID,
				Service: svc.Name,
				Driver:  driver,
				Request: raw,
			}, audit)
			if err != nil {
				return nil, err
			}
			out.Builds = append(out.Builds, buildView(rec, app.Name))
			continue
		}
		rec, err := s.st.CreateBuildAs(ctx, state.BuildRecord{
			ID:      id,
			AppID:   app.ID,
			Service: svc.Name,
			Driver:  driver,
			Request: raw,
		}, &audit)
		if err != nil {
			return nil, err
		}
		out.Builds = append(out.Builds, buildView(rec, app.Name))
	}
	if req.GetService() != "" && len(out.Builds) == 0 && len(out.Passthrough) == 0 {
		return nil, statusInvalidArgument(
			fmt.Sprintf("service %q not found in compose %q", req.GetService(), spec.Name))
	}
	out.Warnings = composeWarnings(warnings)
	return out, nil
}

// resolveBuildContext 解析服务的构建上下文目录并执行包含性校验（H14
// 宿主目录信任边界）：base 为已归一的绝对基准目录，build.context 解析后
// 必须位于 base 之内——`..` 逃逸形态（如 ../../.. 直指宿主根、SQLite 库
// 或密钥材料所在目录）会把宿主任意目录整目录打进镜像再经部署外带，
// 在入队前拒绝（基准为临时回落目录时同样适用：逃逸形态不可能承载合法
// 构建内容，只有外带语义）。复用 compose 族拒绝码 E_COMPOSE_UNSUPPORTED
// （errcode 零新增，H14 裁决），错误信息点名服务与逃逸取值。
func resolveBuildContext(base string, svc compose.Service) (string, error) {
	contextDir := filepath.Join(base, svc.Build.Context) // Join 已 Clean，越界逃逸折叠为前导 ..
	rel, err := filepath.Rel(base, contextDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", apperr.New("E_COMPOSE_UNSUPPORTED",
			"service %s: build.context %q resolves outside the base directory %s: the build context must stay within base_dir (defaults to the server-side temp directory) (host directory trust boundary, H14)",
			svc.Name, svc.Build.Context, base)
	}
	return contextDir, nil
}

// GetBuild 单条构建（CLI build 的等待轮询源）。
func (s *BuildsService) GetBuild(ctx context.Context, req *serverv1.GetBuildRequest) (*serverv1.GetBuildResponse, error) {
	rec, err := s.st.GetBuild(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, state.ErrBuildNotFound) {
			return nil, notFound("build not found: " + req.GetId())
		}
		return nil, err
	}
	return &serverv1.GetBuildResponse{Build: buildView(rec, s.appNameByID(ctx, rec.AppID))}, nil
}

// ListBuilds 按应用列构建（created_at 倒序）。
func (s *BuildsService) ListBuilds(ctx context.Context, req *serverv1.ListBuildsRequest) (*serverv1.ListBuildsResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.st.ListAppBuilds(ctx, app.ID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.BuildView, 0, len(rows))
	for _, rec := range rows {
		out = append(out, buildView(rec, app.Name))
	}
	return &serverv1.ListBuildsResponse{Builds: out}, nil
}

// appNameByID 取应用显示名（已删除应用回退显示 ID——台账对照语义）。
func (s *BuildsService) appNameByID(ctx context.Context, appID string) string {
	if app, err := s.st.GetAppByID(ctx, appID); err == nil {
		return app.Name
	}
	return appID
}

// buildView 构造构建投影。
func buildView(rec state.BuildRecord, appName string) *serverv1.BuildView {
	return &serverv1.BuildView{
		Id:          rec.ID,
		App:         appName,
		Service:     rec.Service,
		Driver:      string(rec.Driver),
		Status:      string(rec.Status),
		ImageRef:    rec.ImageRef,
		ImageDigest: rec.ImageDigest,
		PlanPath:    rec.PlanPath,
		LogPath:     rec.LogPath,
		ErrorCode:   rec.ErrorCode,
		StartedAt:   tstamp(rec.StartedAt),
		FinishedAt:  tstamp(rec.FinishedAt),
	}
}

// composeWarnings 构造 compose 校验警告的跨面投影。
func composeWarnings(ws []compose.Warning) []*serverv1.ComposeWarning {
	out := make([]*serverv1.ComposeWarning, 0, len(ws))
	for _, w := range ws {
		out = append(out, &serverv1.ComposeWarning{
			Kind: w.Kind, Code: w.Code, Service: w.Service, Message: w.Message,
		})
	}
	return out
}
