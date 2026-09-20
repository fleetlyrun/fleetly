package runtime

// 日志管线服务的 lynx.Service 装配壳（T2.20）：Start 阶段进入采集主循环
//（扫描 active apps 受管服务 → 轮询 docker service logs → 脱敏 → ring +
// 落盘 + 扇出）。采集故障为降级语义（warn 日志、下轮重试），不影响
// readiness。Start 阻塞到关停（actor 契约同其他服务壳）。

import (
	"context"

	"github.com/lynx-go/lynx"

	"github.com/fleetlyrun/fleetly/internal/logs"
)

// logsService 是日志采集服务壳。
type logsService struct {
	mg *logs.Manager
}

func newLogsService(mg *logs.Manager) lynx.Service { return logsService{mg: mg} }

func (s logsService) Name() string                 { return "logs.collector" }
func (s logsService) Init(_ lynx.AppContext) error { return nil }

func (s logsService) Start(ctx context.Context) error {
	return s.mg.Run(ctx)
}

// Stop 无资源动作：Run 随服务 ctx 取消返回（在途轮询随 ctx 排水）。
func (s logsService) Stop(_ context.Context) error { return nil }
