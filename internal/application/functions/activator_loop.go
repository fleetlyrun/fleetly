// activator_loop.go 是唤醒组件的投影环（P2-1a 第三条 LoopSpec "activator"，
// 设计 v2 §1.6：functions 模块多环——投影输入 = 活跃项目集（per-project
// 一枚 Workload，System 作者权），载体现 = 运行 activator 二进制、挂全部
// 活跃项目网（traefik 同款挂靠面）、监听 8090。期望集经 recordSpecs 落账
// ——合成路由 parked 分支的 Addresses 解析消费此账本）。
package functions

import (
	"context"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

const (
	// activatorImage 是唤醒组件镜像（钉版；与 caddy/traefik 预载纪律同族
	// ——e2e dind 零外网拉取）。
	activatorImage = "ghcr.io/fleetlyrun/activator:0.1.0"
	// activatorListen 是请求面监听口（cmd/activator ACTIVATOR_LISTEN 缺省
	// 同源——engine functionRoutes.go activatorPort 与 Workload Ports 同参）。
	activatorListen = int32(8090)
	// activatorProbeTimeout 是探针节拍（TCP 探声明口）。
	activatorProbeInterval = 10 * time.Second
)

// stepActivator 是唤醒组件收敛步：列活跃项目 → per-project 一枚 activator
// Workload → 签名短路 → Ensure → recordSpecs（parked 分支解析面）。
func (f *Functions) stepActivator(ctx context.Context) {
	projects, err := f.repos.Projects.List(ctx)
	if err != nil {
		f.log.Error("activator step: list projects", "err", err)
		return
	}
	unlockMaintenance := f.lockMaintenance()
	defer unlockMaintenance()
	stepCtx, cancel := f.boundedStep(ctx)
	defer cancel()

	ws := make([]capability.Workload, 0, len(projects))
	for _, p := range projects {
		ws = append(ws, activatorWorkload(p.ID))
	}
	sig := contract.ManagedFingerprint(ws)
	now := f.clock.Now()
	if _, fresh := f.ensureFresh(f.activatorMemo, "activator", sig, now); fresh {
		return
	}
	if _, err := f.runtime.Ensure(stepCtx, capability.NamespaceRef{}, ws, capability.Generation(0), capability.Materials{}); err != nil {
		f.log.Error("activator step: ensure", "err", err)
		return
	}
	f.ensureRemember(f.activatorMemo, "activator", contract.Memo{Sig: sig, At: now})
	// 期望集落账（parked 分支 Addresses 解析消费；owner = system 域——
	// activator 是平台组件非用户聚合）。
	f.recordSpecs("", ws)
}

// activatorWorkload 投影 per-project 唤醒组件载体（Workload ID =
// activator-<projectID>——与函数行 id 形态区分；挂全部活跃项目网在
// NamespaceRef 空集形态下由 Runtime 侧跨域挂靠承载——traefik 同款）。
func activatorWorkload(projectID string) capability.Workload {
	return capability.Workload{
		ID:       "activator-" + projectID,
		Process:  "activator",
		Image:    activatorImage,
		Replicas: 1,
		Restart:  capability.RestartAlways,
		Ports:    []capability.WorkloadPort{{Port: activatorListen, Protocol: capability.ProtocolHTTP}},
		Healthcheck: &capability.Healthcheck{
			TCPPort:  activatorListen,
			Interval: activatorProbeInterval,
			Timeout:  5 * time.Second,
			Retries:  3,
		},
		// 平台事实 env（唤醒面朝控制面拨号——FLEETLY_ADDR 是控制面可达
		// 地址；受管组件面先例 console/api 同族）。
		Env: map[string]string{
			"FLEETLY_ADDR":     "fleetly-internal:9080",
			"ACTIVATOR_LISTEN": ":8090",
		},
	}
}
