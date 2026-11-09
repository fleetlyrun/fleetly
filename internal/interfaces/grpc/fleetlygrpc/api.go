package fleetlygrpc

// 模块消费面接口（DIP/ISP，2026-11-10）：handler 只依赖本文件按业务模块
// 收窄的小接口，具体 *engine.Engine 只在 Deps 构造面出现（NewServices
// 以结构类型把同一实例赋给各字段）。方法集按 handler 文件的消费归组；
// 跨模块共用方法归语义最近的一侧（如 PublishRoutesNow 归 System 的收敛
// kick 族、NodePlatformIDByCarrier 归 Nodes 的载体解析族）。
//
// 接口方法签名逐字对齐 engine 导出方法（类型别名原样引用，零适配层）；
// assembly 侧有编译期断言钉住 *engine.Engine 对每个接口的实现关系。

import (
	"context"
	"io"

	"github.com/fleetlyrun/fleetly/internal/application/dataservices"
	"github.com/fleetlyrun/fleetly/internal/application/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/schedule"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
)

// DeployAPI 是部署收敛面：提交/取消/回滚/重启/扩缩/预览/渲染/拆除
// （delivery、preview、webhook 与 app 拆除 handler 的消费面）。
type DeployAPI interface {
	SubmitSpec(ctx context.Context, req engine.SubmitSpecRequest) (*revision.Revision, *deployment.Deployment, *engine.Admission, error)
	Cancel(ctx context.Context, id string) (*deployment.Deployment, error)
	Rollback(ctx context.Context, appID, toRevisionID string, confirm *engine.ConfirmInput) (*deployment.Deployment, *engine.Admission, error)
	RestartProcess(ctx context.Context, appID, process string) error
	ScaleProcess(ctx context.Context, appID, process string, replicas int64) (*deployment.Deployment, *engine.Admission, error)
	RenderWorkloads(ctx context.Context, appID string, seq int64) (*revision.Revision, []capability.Workload, error)
	InspectAppWorkloads(ctx context.Context, appID string) ([]capability.WorkloadObservation, error)
	PreviewSpec(ctx context.Context, req engine.SubmitSpecRequest) (*engine.PreviewSpecResult, error)
	ApplyVolumePinning(ctx context.Context, ws []capability.Workload, projectID string) error
	ConfirmBaseSnapshot(ctx context.Context, appID string) (governance.ConfirmBase, error)
	TeardownAnchors(ctx context.Context, appID string) (governance.ConfirmBase, error)
	TeardownCascade(ctx context.Context, appID string) ([]engine.TeardownCascadeEntry, error)
	ObservationSnapshot(ctx context.Context, appID string) []capability.WorkloadEvent
	TeardownApp(ctx context.Context, appID string, confirm *engine.ConfirmInput) error
}

// TaskAPI 是自动化面：task/run/schedule 动词与收敛环唤醒
// （automation handler 的消费面）。
type TaskAPI interface {
	KickTasks()
	KickSchedules()
	ScaleTask(ctx context.Context, id string, desired int64) (*task.Task, error)
	StopTask(ctx context.Context, id string, force bool) (*task.Task, error)
	DeleteTask(ctx context.Context, id string) error
	RenewTask(ctx context.Context, id string) (*task.Task, error)
	StopRun(ctx context.Context, id string) (*run.Run, error)
	TriggerSchedule(ctx context.Context, id string) (*schedule.Schedule, error)
}

// ExecAPI 是 exec 会话面：子面检查/受理/实例路由/会话附着
// （exec handler 与 assembly WS 适配器的消费面）。
type ExecAPI interface {
	ExecUnsupported() bool
	CreateSession(ctx context.Context, in engine.CreateExecInput) (*engine.ExecSessionInfo, error)
	AbandonSession(id string)
	ExecInstances(ctx context.Context, appID, process string) ([]capability.ExecTargetInstance, error)
	ExecSessionByID(id string) (*engine.ExecSessionInfo, bool)
	AttachClient(ctx context.Context, sessionID string, pipe engine.ExecClientPipe) error
}

// BrowseAPI 是 browse 会话面：配置/配额/注册/入口 URL/票据 grant
// （databases browse handler 与 assembly browse 原生入口的消费面）。
type BrowseAPI interface {
	BrowseConfigured() bool
	BrowseCheckQuota(teamID string) error
	RegisterBrowseSession(in engine.BrowseInput) (engine.BrowseSessionInfo, error)
	BrowseEntryURL(sessionID string) string
	BrowseSessionGrant(sessionID string) (string, int, bool)
	BrowseValidateGrant(sessionID, grant string) bool
}

// BackupAPI 是备份面：校验/下载/平台快照与备份环唤醒
// （databases、pubsubs、structure 下载与 platform handler 的消费面）。
type BackupAPI interface {
	VerifyBackup(ctx context.Context, backupID string) (bool, string, error)
	DownloadBackup(ctx context.Context, databaseID, backupID string) (*backup.Backup, io.ReadCloser, error)
	TriggerPlatformBackup(ctx context.Context) (*engine.PlatformSnapshot, error)
	ListPlatformSnapshots(ctx context.Context) ([]engine.PlatformSnapshot, error)
	KickBackups()
}

// RegistryAPI 是镜像仓库与构建日志面：catalog/tags/git-head/clone/
// build 日志回读（registry、webhook 与 delivery 日志 follow 的消费面）。
type RegistryAPI interface {
	RegistryCatalog(ctx context.Context, projectID string) ([]string, error)
	RegistryTags(ctx context.Context, projectID, repository string) ([]capability.RegistryTag, error)
	ResolveGitHead(ctx context.Context, projectID, repoURL, branch string) (string, error)
	CloneAt(ctx context.Context, projectID, repoURL, ref, dir string) error
	RecentBuildLogsAfter(buildID string, after int64) ([]capability.LogFrame, int64)
}

// DataAPI 是数据服务面：database/pubsub/bucketstore 的拆除/轮换/更新/
// 活体检视与三收敛环唤醒（databases、pubsubs、bucketstores 及其子资源
// handler 的消费面；引擎目录读面由模板注册表直供，不经 engine）。
type DataAPI interface {
	CheckVolumeOrigin(ctx context.Context, kind, projectID, name, engineName string) error
	KickDatabases()
	TeardownDatabase(ctx context.Context, id string) error
	RotateDatabasePassword(ctx context.Context, databaseID string) error
	UpdateDatabase(ctx context.Context, databaseID string, req dataservices.UpdateRequest) (dataservices.UpdateResult, error)
	KickPubSubs()
	TeardownPubSub(ctx context.Context, id string) error
	RotatePubSubPassword(ctx context.Context, pubsubID string) error
	UpdatePubSub(ctx context.Context, pubsubID string, req dataservices.PubSubUpdateRequest) (dataservices.UpdateResult, error)
	TopicLiveInspect(ctx context.Context, pubsubID, topicID string) (pubsubtemplate.TopicLive, error)
	StreamLiveInspect(ctx context.Context, pubsubID, streamID string) (pubsubtemplate.StreamLive, error)
	KickBucketStores()
	TeardownBucketStore(ctx context.Context, id string) error
	RotateBucketStorePassword(ctx context.Context, bucketStoreID string) error
	UpdateBucketStore(ctx context.Context, bucketStoreID string, req dataservices.BucketStoreUpdateRequest) (dataservices.UpdateResult, error)
	BucketLiveInspect(ctx context.Context, bucketStoreID, bucketID string) (bucketstoretemplate.BucketLive, error)
}

// NodeAPI 是节点/网络运行面：relay 凭证与附着、载体节点解析、网络重建
// 与对等隔离（contexts/nodes、networks handler 与 assembly relay 原生
// 入口的消费面）。
type NodeAPI interface {
	RelayStatuses() map[string]engine.RelayStatus
	DropRelay(platformID string)
	DropAllRelays()
	ValidateRelayCredential(ctx context.Context, bearer string) error
	RelayAttach(ctx context.Context, token, carrierNodeID, relayVersion string, w engine.RelayWriter) (func(capability.RelayFrame), func(), error)
	NodePlatformIDByCarrier(ctx context.Context, carrierNodeID string) (string, error)
	RebuildNetwork(ctx context.Context, projectID, name string) (engine.NetworkRebuildResult, error)
	RequestPeerIsolation(networkID, peerProjectID string)
}

// SystemAPI 是系统观测/告警/诊断面：告警规则与通知通道 repo 访问器、
// 系统告警状态、诊断事实、受管环与路由发布唤醒
// （alerting、diagnosis、structure 与 proxy route handler 的消费面）。
type SystemAPI interface {
	AlertRuleRepo() alertrule.Repo
	NotificationChannelRepo() channel.Repo
	SystemConvergeAlertState() engine.SystemAlertState
	SystemOffsiteAlertState() engine.SystemAlertState
	TestNotificationChannel(ctx context.Context, channelID string) error
	DiagnoseFacts(ctx context.Context, appID string) diagnosis.Facts
	DiagnoseAppCheapHit(appID string) bool
	KickManagedLoop()
	PublishRoutesNow()
}

// writeAPI 是受理位写原语（acceptance.go 的 commit 唯一消费面，ADR-0087
// 决策 4）：全部业务模块共用的写事实入口，不归任一模块，故只以 Services
// 私有字段持有，不进模块接口面。
type writeAPI interface {
	Commit(ctx context.Context, f engine.WriteFact) error
}
