package fleetlygrpc

// 等待原语（F1.3，架构 §7）：事件流过滤实现——outbox 事件只是信号，行
// 重读是真源（快照帧永远如实）；复用 subscribeEvents 单一订阅核心（订阅
// 面/等待面同源，ADR-0026 纪律）。流式面不经 unary 超时拦截器，生命周期
// 由取消信号管理（架构 §7：Agent 的"部署-等待-验证"循环不自写轮询）。
// WaitRun 随 Run 实体（F1.5/6）落地；waitOnRow 是 ADR-0063 收编三 verb
// 手搓 send 闭包的唯一循环。

import (
	"context"
	"errors"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/build"
)

// errWaitDone 是 send 侧的收流哨兵：终态帧已发，订阅循环应结束且对外
// 不是错误。
var errWaitDone = errors.New("wait: terminal frame delivered")

// waitOnRow 是「读行 → 状态未变不重发 → 发帧 → 终态帧后收流」的唯一循环。
// 各聚合 verb 只供四个 variation points：行读（含域词错误映射）、状态键、
// 行型既有 Terminal() 谓词、帧构造。终态判定单源于 state 层谓词——verb
// 不再内联状态集（WaitDeployment 曾手搓四值 switch 与 Terminal() 平行漂移）。
func waitOnRow[Row any](ctx context.Context, s *Services, aggregate, id string,
	read func(context.Context) (Row, error),
	stateOf func(Row) string,
	terminal func(Row) bool,
	sendFrame func(Row) error,
) error {
	last := ""
	send := func() error {
		row, err := read(ctx)
		if err != nil {
			return err
		}
		if stateOf(row) == last {
			return nil // 状态未变不重发（事件只是信号，帧只走状态迁移）
		}
		last = stateOf(row)
		if err := sendFrame(row); err != nil {
			return err
		}
		if terminal(row) {
			return errWaitDone
		}
		return nil
	}
	return waitOnAggregate(ctx, s, aggregate, id, send)
}

// WaitDeployment：逐状态快照帧，终态（succeeded/failed/superseded/
// cancelled = State.Terminal()）帧后收流。
func (svc *DeploymentsService) WaitDeployment(req *deliveryv1.WaitDeploymentRequest, stream deliveryv1.DeploymentsService_WaitDeploymentServer) error {
	if req.GetDeploymentId() == "" {
		return apperr.New("E_INVALID_ARGUMENT", "deployment_id: must not be empty")
	}
	ctx := stream.Context()
	// 行级授权（ADR-0035）：流面前置（首帧行读即 404 的语义不变）。
	if err := svc.s.authorizeDeploymentID(ctx, req.GetDeploymentId()); err != nil {
		return err
	}
	return waitOnRow(ctx, svc.s, "deployment", req.GetDeploymentId(),
		func(ctx context.Context) (*deployment.Deployment, error) {
			d, err := svc.s.Deployments.Get(ctx, svc.s.DB.Runner(), req.GetDeploymentId())
			if err != nil {
				return nil, mapStateError(err, "deployment")
			}
			return d, nil
		},
		func(d *deployment.Deployment) string { return string(d.State) },
		func(d *deployment.Deployment) bool { return d.State.Terminal() },
		func(d *deployment.Deployment) error {
			return stream.Send(&deliveryv1.WaitDeploymentResponse{Deployment: deploymentMsg(*d)})
		})
}

// WaitBuild：同构（终态 = build.State.Terminal()）。
func (svc *BuildsService) WaitBuild(req *deliveryv1.WaitBuildRequest, stream deliveryv1.BuildsService_WaitBuildServer) error {
	if req.GetBuildId() == "" {
		return apperr.New("E_INVALID_ARGUMENT", "build_id: must not be empty")
	}
	ctx := stream.Context()
	// 行级授权（ADR-0035）：同构前置。
	if err := svc.s.authorizeBuildID(ctx, req.GetBuildId()); err != nil {
		return err
	}
	return waitOnRow(ctx, svc.s, "build", req.GetBuildId(),
		func(ctx context.Context) (*build.Build, error) {
			b, err := svc.s.Builds.Get(ctx, svc.s.DB.Runner(), req.GetBuildId())
			if err != nil {
				return nil, mapStateError(err, "build")
			}
			return b, nil
		},
		func(b *build.Build) string { return string(b.State) },
		func(b *build.Build) bool { return b.State.Terminal() },
		func(b *build.Build) error {
			return stream.Send(&deliveryv1.WaitBuildResponse{Build: buildMsg(*b)})
		})
}

// waitOnAggregate：首帧（当前行快照）→ 从订阅起点跟随 aggregate/id 命中
// 重读行发帧。订阅起点先于行读：两步间落下的事件只多触发一次幂等重读。
// 行不存在在首帧即 E_NOT_FOUND（Deployment/Build 行永不删除）。
func waitOnAggregate(ctx context.Context, s *Services, aggregate, id string, send func() error) error {
	cursor, err := s.OutboxEvents.LastSeq(ctx, s.DB.Runner())
	if err != nil {
		return mapStateError(err, "events")
	}
	// 订阅按调用方 Team 过滤（P0-2）：等待对象已过行级授权，本队/平台级
	// 事件照常命中，他队事件不再流经等待面。
	team, terr := eventsTeamFilterOf(ctx)
	if terr != nil {
		return terr
	}
	if err := send(); err != nil {
		if errors.Is(err, errWaitDone) {
			return nil
		}
		return err
	}
	err = s.subscribeEvents(ctx, team, cursor, true, func(ev *telemetryv1.Event) error {
		if ev.Aggregate != aggregate || ev.AggregateId != id {
			return nil
		}
		return send()
	})
	if errors.Is(err, errWaitDone) {
		return nil
	}
	return err
}
