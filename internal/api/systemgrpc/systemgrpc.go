// Package systemgrpc 实现 fleetly.system.v1.SystemService：控制面自身
// 状态面（`fleetly status` / `fleetly doctor` 消费）。
package systemgrpc

import (
	"context"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/buildinfo"
)

// Service 满足 systemv1.SystemServiceServer。状态判定目前只看进程存活
// （控制面起来了即 healthy）；Capability Health() 降级矩阵接入后，
// DEGRADED 由 checkers 聚合派生（F0.19 随 swarm Provider 落地）。
type Service struct {
	systemv1.UnimplementedSystemServiceServer

	info buildinfo.BuildInfo
}

// New 构造 SystemService。
func New(info buildinfo.BuildInfo) *Service {
	return &Service{info: info}
}

// GetVersion 返回控制面构建信息。
func (s *Service) GetVersion(context.Context, *systemv1.GetVersionRequest) (*systemv1.GetVersionResponse, error) {
	return &systemv1.GetVersionResponse{
		Version: s.info.Version,
		Commit:  s.info.Commit,
		Date:    s.info.Date,
	}, nil
}

// GetStatus 返回控制面服务状态。
func (s *Service) GetStatus(context.Context, *systemv1.GetStatusRequest) (*systemv1.GetStatusResponse, error) {
	return &systemv1.GetStatusResponse{
		State:   systemv1.StatusState_STATUS_STATE_HEALTHY,
		Version: s.info.Version,
	}, nil
}
