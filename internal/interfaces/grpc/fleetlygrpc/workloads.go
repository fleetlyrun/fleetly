package fleetlygrpc

import (
	"context"
	"errors"

	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
)

// WorkloadsService 是载体级在服观测的只读透出（ADR-0071 观测层）："集群
// 里真跑着什么"——与 delivery 域 GetAppWorkloads 的投影视图（"平台会
// 下发什么"）互补对照，drift 一眼可见。观测与 drift 扫描同一数据面
// （ADR-0022 InspectWorkloads），本面只读不参与决策。
type WorkloadsService struct {
	runtimev1.UnimplementedWorkloadsServiceServer
	s *Services
}

func (svc *WorkloadsService) GetAppWorkloadObservations(ctx context.Context, req *runtimev1.GetAppWorkloadObservationsRequest) (*runtimev1.GetAppWorkloadObservationsResponse, error) {
	if req.GetAppId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "app_id: must not be empty")
	}
	if err := svc.s.authorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	obs, err := svc.s.Engine.InspectAppWorkloads(ctx, req.GetAppId())
	if err != nil {
		if errors.Is(err, engine.ErrRuntimeNoInspect) {
			return nil, apperr.New("E_INTERNAL", "the runtime provider does not implement workload inspection").WithCause(err)
		}
		return nil, mapStateError(err, "workloads")
	}
	out := &runtimev1.GetAppWorkloadObservationsResponse{}
	for _, o := range obs {
		out.Observations = append(out.Observations, workloadObservationMsg(o))
	}
	return out, nil
}
