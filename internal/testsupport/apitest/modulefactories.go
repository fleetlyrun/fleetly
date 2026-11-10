package apitest

// 应用模块工厂接线（engine 去应用模块 import 第一~九批，2026-11-10）：
// 夹具与生产同源——组合根注入具体装配器，engine 核只持 contract 接口；
// 端口袋与各应用模块 Deps 的形状由模块包内编译期互转断言钉死。接线独立
// 成文件，harness.go 守住体积红线（sizeguard 1200 行）。

import (
	"github.com/fleetlyrun/fleetly/internal/application/backups"
	"github.com/fleetlyrun/fleetly/internal/application/browses"
	"github.com/fleetlyrun/fleetly/internal/application/dataservices"
	"github.com/fleetlyrun/fleetly/internal/application/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/execs"
	"github.com/fleetlyrun/fleetly/internal/application/intake"
	"github.com/fleetlyrun/fleetly/internal/application/logpipes"
	"github.com/fleetlyrun/fleetly/internal/application/manageds"
	"github.com/fleetlyrun/fleetly/internal/application/metrics"
)

// newDiagnosisModule 是诊断应用模块工厂（engine 去应用模块 import 试点，
// 2026-11-10：读时装配器，无环无写面）。
func newDiagnosisModule(p contract.DiagnosisPorts) contract.Diagnosis {
	return diagnosis.New(diagnosis.Deps{
		Clock:               p.Clock,
		Outbox:              p.Outbox,
		AlertRules:          p.AlertRules,
		Deployments:         p.Deployments,
		ObservationSnapshot: p.ObservationSnapshot,
		WorkloadProcess:     p.WorkloadProcess,
	})
}

// newLoggingModule 是采集循环应用模块工厂（第二批；rootCtx/spawn 即引擎
// 生命周期端口）。
func newLoggingModule(p contract.LoggingPorts) contract.Logging {
	return logpipes.New(logpipes.Deps{
		Logging: p.Logging, Runtime: p.Runtime, Proxy: p.Proxy, Registry: p.Registry,
		Repos: p.Repos, Log: p.Log, Clock: p.Clock, Cursors: p.Cursors,
		RootCtx: p.RootCtx, Spawn: p.Spawn,
	})
}

// 受管/Browse/指标/exec/构建摄取/Backup/数据服务应用模块工厂（第三/四/五/六/七/八/九批）：
// 端口袋与 Deps 逐字段同形，直接互转（模块包内 var 断言钉死两侧漂移）。
func newManagedModule(p contract.ManagedPorts) contract.Managed {
	return manageds.New(manageds.Deps(p))
}
func newBrowseModule(p contract.BrowsePorts) contract.Browse    { return browses.New(browses.Deps(p)) }
func newMetricsModule(p contract.MetricsPorts) contract.Metrics { return metrics.New(metrics.Deps(p)) }
func newExecModule(p contract.ExecPorts) contract.Exec          { return execs.New(execs.Deps(p)) }
func newIntakeModule(p contract.IntakePorts) contract.Intake    { return intake.New(intake.Deps(p)) }
func newBackupModule(p contract.BackupPorts) contract.Backup    { return backups.New(backups.Deps(p)) }
func newDataServiceModule(p contract.DataServicePorts) contract.DataService {
	return dataservices.New(dataservices.Deps(p))
}
