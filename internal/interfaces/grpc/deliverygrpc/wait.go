package deliverygrpc

import (
	"context"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// waitOnRow 是「读行 → 状态未变不重发 → 发帧 → 终态帧后收流」的唯一循环。
// 各聚合 verb 只供四个 variation points：行读（含域词错误映射）、状态键、
// 行型既有 Terminal() 谓词、帧构造。终态判定单源于 state 层谓词——verb
// 不再内联状态集（WaitDeployment 曾手搓四值 switch 与 Terminal() 平行漂移）。
func waitOnRow[Row any](ctx context.Context, src grpcapi.WaitSource, aggregate, id string,
	read func(context.Context) (Row, error),
	stateOf func(Row) string,
	terminal func(Row) bool,
	sendFrame func(Row) error,
) error {
	return grpcapi.WaitOnRow(ctx, src, aggregate, id, read, stateOf, terminal, sendFrame)
}

// WaitDeployment：逐状态快照帧，终态（succeeded/failed/superseded/
// cancelled = State.Terminal()）帧后收流。
func (svc *DeploymentsService) WaitDeployment(req *deliveryv1.WaitDeploymentRequest, stream deliveryv1.DeploymentsService_WaitDeploymentServer) error {
	if req.GetDeploymentId() == "" {
		return apperr.New("E_INVALID_ARGUMENT", "deployment_id: must not be empty")
	}
	ctx := stream.Context()
	// 行级授权（ADR-0035）：流面前置（首帧行读即 404 的语义不变）。
	if err := svc.deps.Auth.AuthorizeDeploymentID(ctx, req.GetDeploymentId()); err != nil {
		return err
	}
	return waitOnRow(ctx, svc.deps.Wait, "deployment", req.GetDeploymentId(),
		func(ctx context.Context) (*deployment.Deployment, error) {
			d, err := svc.deps.Deployments.Get(ctx, req.GetDeploymentId())
			if err != nil {
				return nil, grpcapi.MapStateError(err, "deployment")
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
	if err := svc.deps.Auth.AuthorizeBuildID(ctx, req.GetBuildId()); err != nil {
		return err
	}
	return waitOnRow(ctx, svc.deps.Wait, "build", req.GetBuildId(),
		func(ctx context.Context) (*build.Build, error) {
			b, err := svc.deps.Builds.Get(ctx, req.GetBuildId())
			if err != nil {
				return nil, grpcapi.MapStateError(err, "build")
			}
			return b, nil
		},
		func(b *build.Build) string { return string(b.State) },
		func(b *build.Build) bool { return b.State.Terminal() },
		func(b *build.Build) error {
			return stream.Send(&deliveryv1.WaitBuildResponse{Build: buildMsg(*b)})
		})
}
