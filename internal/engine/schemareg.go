package engine

// 事件 payload schema 自描述注册（F1.4，架构 §7"扩展面各自注入合并"）：
// engine 是部署链/构建链/观测链事件 payload 形状的拥有方，在本 init 把
// events.go 的 payload 结构体反射进 internal/schema 注册表。事件名字面量
// 同时是 usage 反扫锚；summary 单源取 eventcode 注册表。完备性（在册事件
// 必有 schema）由 internal/assembly 的守卫对账——新事件入册而漏注册即红。

import (
	"github.com/fleetlyrun/fleetly/internal/model/eventcode"
	"github.com/fleetlyrun/fleetly/internal/schema"
)

// registerEventPayload 登记一个事件名的 payload schema（summary 取
// eventcode 单源；未在册事件名在此 panic——schema 面不先于注册表存在）。
func registerEventPayload(name string, payload any) {
	e, ok := eventcode.Get(name)
	if !ok {
		panic("engine: schema registration for event " + name + " which is not in the eventcode registry")
	}
	schema.Register(schema.KindEvent, name, e.Summary, schema.Reflect(payload))
}

func init() {
	// Deployment 状态机（deployment.*，统一 payload 形态）。
	for _, name := range []string{
		"deployment.queued",
		"deployment.preparing",
		"deployment.building",
		"deployment.releasing",
		"deployment.observing",
		"deployment.succeeded",
		"deployment.failed",
		"deployment.rolling_back",
		"deployment.superseded",
		"deployment.cancelled",
	} {
		registerEventPayload(name, deploymentEventPayload{})
	}
	// firstBootJobs 链接线（ADR-0030）：铸造事件 + 部署因果链（job 生命周期
	// 观测面复用 task.*/run.* 既有注册）。
	registerEventPayload(eventFirstBootJobFired, firstBootJobEventPayload{})
	// Build 状态机（build.*）。
	for _, name := range []string{
		"build.queued",
		"build.building",
		"build.succeeded",
		"build.failed",
		"build.cancelled",
		"build.expired",
	} {
		registerEventPayload(name, buildEventPayload{})
	}
	// 观测链（workload.* / node.*）。
	registerEventPayload(eventWorkloadDrift, driftEventPayload{})
	registerEventPayload(eventWorkloadStopped, stoppedEventPayload{})
	registerEventPayload(eventWorkloadRolloutStall, rolloutStalledEventPayload{})
	registerEventPayload(eventNodeJoined, nodeEventPayload{})
	registerEventPayload("node.left", nodeEventPayload{})

	// Task 状态机（task.*，统一 payload 形态）。
	for _, name := range []string{
		EventTaskCreated,
		"task.active",
		eventTaskUpdated,
		eventTaskDraining,
		"task.completed",
		"task.failed",
		"task.drained",
		eventTaskDeleted,
	} {
		registerEventPayload(name, taskEventPayload{})
	}
	// Run 状态机（run.*）。
	for _, name := range []string{
		"run.created",
		"run.running",
		"run.stopping",
		"run.stopped",
		"run.failed",
	} {
		registerEventPayload(name, runEventPayload{})
	}
	// Owner Lease（lease.*，task 聚合）。
	registerEventPayload(eventLeaseRenewed, leaseEventPayload{})
	registerEventPayload(eventLeaseExpired, leaseEventPayload{})
	// Schedule 状态机（schedule.*，统一 payload 形态，F1.7）。
	for _, name := range []string{
		EventScheduleCreated,
		eventScheduleFired,
		eventScheduleSkipped,
		eventScheduleDeleted,
	} {
		registerEventPayload(name, scheduleEventPayload{})
	}
	// Backup 执行链（F2.2，ADR-0039；restored 独立 payload 形态）。
	registerEventPayload(eventBackupSucceeded, backupEventPayload{})
	registerEventPayload(eventBackupFailed, backupEventPayload{})
	registerEventPayload(eventDatabaseRestored, databaseRestoredEventPayload{})
	registerEventPayload(eventPlatformBackupOK, platformBackupEventPayload{})
	registerEventPayload(eventPlatformBackupFail, platformBackupEventPayload{})
	// 阈值告警（F2.5，ADR-0041 决策 3：迁移沿才发；channel_failed 独立
	// 诊断载荷）。
	registerEventPayload(eventAlertFired, alertEventPayload{})
	registerEventPayload(eventAlertResolved, alertEventPayload{})
	registerEventPayload(eventAlertChannelFail, alertChannelFailedPayload{})
}
