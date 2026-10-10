package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// 组件重启的哨兵错误（systemgrpc 面 map 为精确错误码；engine 不 import
// api 包——依赖方向，见架构 §2）。
var (
	// ErrComponentUnknown 是未知受管 Provider 名。
	ErrComponentUnknown = errors.New("component: unknown managed provider name")
	// ErrRestartUnsupported 是该 Provider 不受管自宿（无部署形态声明，
	// 无载体可重启）。
	ErrRestartUnsupported = errors.New("component: provider has no managed workloads to restart")
	// ErrRuntimeNoRestart 是 Runtime Provider 未实现重启子面（capability
	// .RuntimeRestart 可选子面——swarm/k3s 在册，第三 runtime 缺席时精确拒绝）。
	ErrRuntimeNoRestart = errors.New("component: runtime provider does not implement carrier restart")
)

// RestartComponent 重启受管组件载体（IA v3 二期④，排障动线"日志断了 →
// 就地重启"）：按 Provider 名在受管自宿声明者（capability.Managed，
// Describe().Managed 契约）中路由，经 Runtime 可选重启子面逐 Workload
// 强制重排。返回重启的载体数。
func (e *Engine) RestartComponent(ctx context.Context, name string) (int, error) {
	if name == "" {
		return 0, fmt.Errorf("%w: name must not be empty", ErrComponentUnknown)
	}
	providers := []capability.Provider{e.runtime, e.proxy, e.registry, e.logging, e.metrics, e.objectStore}
	found := false
	var managed capability.Managed
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		if provider.Describe().Name == name {
			found = true
			managed, _ = provider.(capability.Managed)
			break
		}
	}
	if !found {
		return 0, ErrComponentUnknown
	}
	if managed == nil {
		return 0, ErrRestartUnsupported
	}
	restart, ok := e.runtime.(capability.RuntimeRestart)
	if !ok {
		return 0, ErrRuntimeNoRestart
	}
	ns := managed.ManagedNamespace()
	ws := managed.ManagedWorkloads()
	if len(ws) == 0 {
		return 0, ErrRestartUnsupported
	}
	restarted := 0
	for _, w := range ws {
		if err := restart.Restart(ctx, ns, w.ID); err != nil {
			return restarted, err
		}
		restarted++
	}
	return restarted, nil
}
