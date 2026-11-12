package grpcapi

import (
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
)

// AdmissionMsg 是 Submit 判定附注的响应投影（nil 安全——engine 旧路径
// 缺附注时投影零值面由调用方豁免）。
func AdmissionMsg(a *api.Admission) *deliveryv1.Admission {
	if a == nil {
		return nil
	}
	return &deliveryv1.Admission{
		Outcome: string(a.Outcome),
		//nolint:gosec // 位次域 = 活跃部署数 + 1（排队容量上限内，远小于 int32）
		Position: int32(a.Position), ExistingDeployment: a.Existing,
	}
}
