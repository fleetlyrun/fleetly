package fleetlygrpc

// 等待原语（F1.3，架构 §7）：事件流过滤实现——outbox 事件只是信号，行
// 重读是真源（快照帧永远如实）；复用 subscribeEvents 单一订阅核心（订阅
// 面/等待面同源，ADR-0026 纪律）。流式面不经 unary 超时拦截器，生命周期
// 由取消信号管理（架构 §7：Agent 的"部署-等待-验证"循环不自写轮询）。
// WaitRun 随 Run 实体（F1.5/6）落地。

import (
	"context"
	"errors"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// errWaitDone 是 send 侧的收流哨兵：终态帧已发，订阅循环应结束且对外
// 不是错误。
var errWaitDone = errors.New("wait: terminal frame delivered")

// WaitDeployment：逐状态快照帧，终态（succeeded/failed/superseded/
// cancelled）帧后收流。
func (svc *DeploymentsService) WaitDeployment(req *deliveryv1.WaitDeploymentRequest, stream deliveryv1.DeploymentsService_WaitDeploymentServer) error {
	if req.GetDeploymentId() == "" {
		return apperr.New("E_INVALID_ARGUMENT", "deployment_id: must not be empty")
	}
	ctx := stream.Context()
	last := ""
	send := func() error {
		d, err := svc.s.Deployments.Get(ctx, svc.s.DB.Runner(), req.GetDeploymentId())
		if err != nil {
			return mapStateError(err, "deployment")
		}
		if string(d.State) == last {
			return nil // 状态未变不重发（事件只是信号，帧只走状态迁移）
		}
		last = string(d.State)
		if err := stream.Send(&deliveryv1.WaitDeploymentResponse{Deployment: deploymentMsg(*d)}); err != nil {
			return err
		}
		switch d.State {
		case deployment.StateSucceeded, deployment.StateFailed, deployment.StateSuperseded, deployment.StateCancelled:
			return errWaitDone
		}
		return nil
	}
	return waitOnAggregate(ctx, svc.s, "deployment", req.GetDeploymentId(), send)
}

// WaitBuild：同构（终态 = build.State.Terminal()）。
func (svc *BuildsService) WaitBuild(req *deliveryv1.WaitBuildRequest, stream deliveryv1.BuildsService_WaitBuildServer) error {
	if req.GetBuildId() == "" {
		return apperr.New("E_INVALID_ARGUMENT", "build_id: must not be empty")
	}
	ctx := stream.Context()
	last := ""
	send := func() error {
		b, err := svc.s.Builds.Get(ctx, svc.s.DB.Runner(), req.GetBuildId())
		if err != nil {
			return mapStateError(err, "build")
		}
		if string(b.State) == last {
			return nil
		}
		last = string(b.State)
		if err := stream.Send(&deliveryv1.WaitBuildResponse{Build: buildMsg(*b)}); err != nil {
			return err
		}
		if b.State.Terminal() {
			return errWaitDone
		}
		return nil
	}
	return waitOnAggregate(ctx, svc.s, "build", req.GetBuildId(), send)
}

// waitOnAggregate：首帧（当前行快照）→ 从订阅起点跟随 aggregate/id 命中
// 重读行发帧。订阅起点先于行读：两步间落下的事件只多触发一次幂等重读。
// 行不存在在首帧即 E_NOT_FOUND（Deployment/Build 行永不删除）。
func waitOnAggregate(ctx context.Context, s *Services, aggregate, id string, send func() error) error {
	cursor, err := s.OutboxEvents.LastSeq(ctx, s.DB.Runner())
	if err != nil {
		return mapStateError(err, "events")
	}
	if err := send(); err != nil {
		if errors.Is(err, errWaitDone) {
			return nil
		}
		return err
	}
	err = s.subscribeEvents(ctx, cursor, true, func(ev *telemetryv1.Event) error {
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
