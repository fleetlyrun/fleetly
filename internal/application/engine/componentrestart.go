package engine

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// RestartComponent 重启受管组件载体（IA v3 二期④，排障动线"日志断了 →
// 就地重启"）：按 Provider 名在受管自宿声明者（capability.Managed，
// Describe().Managed 契约）中路由，经 Runtime 可选重启扩展接口逐 Workload
// 强制重排。返回重启的载体数。
func (e *Engine) RestartComponent(ctx context.Context, name string) (int, error) {
	if name == "" {
		return 0, fmt.Errorf("%w: name must not be empty", ErrComponentUnknown)
	}
	// P4 wave-1b：Metrics capability 随 metrics 应用模块持有；P5 wave-2 拆包后
	// 经核门面 MetricsProvider 委托读（消费点零改）；objectStore 随 backups
	// 应用模块持有、P5 wave-3 拆包后经应用模块 ObjectStore 访问器读（消费点
	// 零改）；engine 去应用模块 import 第八批起该访问器在 contract.Backup
	// 接口上，工厂缺席（手动测试装配）时跳过（nil provider 不进枚举）。
	var store capability.ObjectStore
	if e.backups != nil {
		store = e.backups.ObjectStore()
	}
	providers := []capability.Provider{e.runtime, e.proxy, e.registry, e.logging, e.MetricsProvider(), store}
	found := false
	var managed capability.Managed
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		if provider.Describe().Name == name {
			found = true
			managed = capability.FacesOf(provider).Managed
			break
		}
	}
	if !found {
		return 0, ErrComponentUnknown
	}
	if managed == nil {
		return 0, ErrRestartUnsupported
	}
	restart := capability.FacesOf(e.runtime).Restart
	if restart == nil {
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
