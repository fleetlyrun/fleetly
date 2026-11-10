// backup.go 是 Backup 域应用模块的核/模块契约面（engine 去应用模块 import
// 第八批，2026-11-10）：engine 核只持 Backup 接口与构造端口袋，具体装配器
// 由组合根经 engine.Deps.BackupFactory 注入——核不再 import backups 应用
// 模块包。值类型/事件名/哨兵随契约面下沉（gRPC platform 组装面与 assembly
// 构造面的既名引用经 engine 别名零改）；端口形态全部落在 domain 面
// （capability/state/repo 签名），契约叶子保持零应用模块类型（ADR-0088
// 决策 7 裁定二）。
package contract

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// ErrPlatformBackupInProgress 是 Platform Backup 已有执行在途（H6 引擎级
// 单飞：快照/仓锁外步骤不得并发，否则可产出损坏但 restic check 通过的
// 恢复点；API 层映射 E_CONFLICT，节拍环静默跳过）。P5 wave-3 随域拆包
// 迁入 backups，engine 去应用模块 import 第八批随契约面下沉——核侧经 var
// 别名零漂移（gRPC 面的 errors.Is 判定零改）。
var ErrPlatformBackupInProgress = errors.New("engine: platform backup already in progress")

// BackupOptions 是备份域配置快照（engine Options.Backup 段三字段 + DataRoot；
// 构造后冻结——P4 嵌入期持核 Options 整体副本，P5 wave-3 拆包起应用模块自持
// 本域切片，语义零漂移；engine 去应用模块 import 第八批随端口袋下沉契约
// 叶子——应用模块包类型不得穿过本叶子）。
type BackupOptions struct {
	// BackupTimeout 是单次备份/恢复执行硬上限（缺省 15m；GB 级 Backup 的
	// 流式执行面——单写者循环卡死即时间看门狗失明，ManagedStepTimeout 同源
	// 教训）。
	BackupTimeout time.Duration
	// PlatformBackup 是控制面 restic 快照配置（nil = Platform Backup 停用
	//——装配缺省注入，nil 只属测试形态；ADR-0039）。
	PlatformBackup *PlatformBackupConfig
	// ResticPath 是 restic 二进制路径缝（测试注入假可执行；空 = 生产
	// exec.LookPath 探测——缺席即 Platform Backup 停用的探测面）。
	ResticPath string
	// DataRoot 是平台数据根（restic 备份集/仓密/节拍锚的落点）。
	DataRoot string
}

// PlatformBackupConfig 是执行链的配置快照（assembly 从 AppConfig 注入；
// interval/retention 缺省在 config 访问器，s3 nil = 仅本地仓）。P5
// wave-3 随域拆包迁入 backups，engine 去应用模块 import 第八批随契约面
// 下沉——assembly/gRPC 构造面经 engine 类型别名零改。
type PlatformBackupConfig struct {
	Interval  time.Duration
	Retention time.Duration
	S3        *S3RepoConfig
}

// S3RepoConfig 是外置仓端点面（同上随契约面下沉）。
type S3RepoConfig struct {
	Endpoint        string
	Bucket          string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
}

// PlatformSnapshot 是 restic snapshots --json 的一行（列举面）。P5
// wave-3 随域迁入 backups，engine 去应用模块 import 第八批随契约面下沉
// ——gRPC 组装面经 engine 类型别名零改。
type PlatformSnapshot struct {
	ID       string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
}

// Backup 事件名锚定（F2.2，ADR-0039；B4a 扩 manifest 面；2026-11-10 主体
// 中性化更名——database.backup_* / database.restored 旧名不再发射，无兼容
// 别名）。usage 反扫的字面量命中点。P5 wave-3 随域拆包迁入 backups，
// engine 去应用模块 import 第八批随契约面下沉——核 schemareg 注册面与
// eventcode 注册表同源（核侧经常量别名零漂移）。
const (
	// EventBackupSucceeded 是单次备份成功（engine 备份循环发射；载荷回执
	// B4a 起 = manifest 锚三元组：object_key=manifest 键 / digest=
	// total_digest / size_bytes=成员合计）。
	EventBackupSucceeded = "backup.succeeded"
	// EventBackupFailed 是单次备份失败（engine 备份循环发射）。
	EventBackupFailed = "backup.failed"
	// EventBackupOrphanSwept 是孤儿对账回收（B4a，红队 A3③：List 对账
	// 删除 manifest 缺失的成员对象——部分成功残留/历史半删态，宽限窗外）。
	EventBackupOrphanSwept = "backup.orphan_swept"
	// EventBackupRestored 是恢复完成（engine 恢复面发射；失败走行
	// restore_error）。
	EventBackupRestored = "backup.restored"
	// EventPlatformBackupOK 是平台备份全链成功（restic 双仓）。
	EventPlatformBackupOK = "platform.backup_succeeded"
	// EventPlatformBackupFail 是平台备份失败（restic 错误尾随载荷）。
	EventPlatformBackupFail = "platform.backup_failed"
)

// BackupEventPayload 是 backup.succeeded/backup.failed 载荷 schema（F2.2，
// ADR-0039；字段只增）。统一形态：成功携带 ObjectStore 回执三元组
// （B4a 起 manifest 锚语义：key/digest/size 见 EventBackupSucceeded 注），
// 失败携带 error（用户可见文本英文）。kind/subject_id 是主体中性轴
// （database/pubsub/bucketstore）。
type BackupEventPayload struct {
	BackupID  string `json:"backup_id"`
	Kind      string `json:"kind"`
	SubjectID string `json:"subject_id"`
	ProjectID string `json:"project_id"`
	Engine    string `json:"engine"`
	ObjectKey string `json:"object_key,omitempty"`
	Digest    string `json:"digest,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Error     string `json:"error,omitempty"`
}

// BackupOrphanSweptEventPayload 是 backup.orphan_swept 载荷 schema（B4a：
// 被回收的对象键清单——对象集前缀在事件 id 轴，键清单是完整事实面）。
type BackupOrphanSweptEventPayload struct {
	Keys []string `json:"keys"`
}

// BackupRestoredEventPayload 是 backup.restored 载荷 schema（恢复完成
// 事实：目标主体 + 源备份）。
type BackupRestoredEventPayload struct {
	Kind       string `json:"kind"`
	SubjectID  string `json:"subject_id"`
	ProjectID  string `json:"project_id"`
	FromBackup string `json:"from_backup"`
	ObjectKey  string `json:"object_key,omitempty"`
}

// PlatformBackupEventPayload 是 platform.backup_* 载荷 schema（restic 链
// 整体成败；快照细节在仓库自身——事件只携带节拍事实）。
type PlatformBackupEventPayload struct {
	Retention string `json:"retention,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Backup 是 Backup 域应用模块的核消费面：engine 循环表经 Module.Loops 取
// backup 步，componentrestart 的 providers 读经 ObjectStore，gRPC Backup/
// platform 面与 lifecycle 启动清扫经其余方法（消费点零改）。
type Backup interface {
	Module
	// ObjectStore 暴露产物承载端口（componentrestart 的 providers 读面）。
	ObjectStore() capability.ObjectStore
	// VerifyBackup manifest 驱动逐件重算（API VerifyBackup 与恢复演练共用）。
	VerifyBackup(ctx context.Context, backupID string) (bool, string, error)
	// DownloadBackup 打开备份产物集的只读回传流（调用方负责 Close）。
	DownloadBackup(ctx context.Context, subjectID, backupID string) (*backup.Backup, io.ReadCloser, error)
	// TriggerPlatformBackup 立即同步执行一次 Platform Backup（手动触发面）。
	TriggerPlatformBackup(ctx context.Context) (*PlatformSnapshot, error)
	// ListPlatformSnapshots 列举本地仓快照（新→旧）。
	ListPlatformSnapshots(ctx context.Context) ([]PlatformSnapshot, error)
	// SweepInterrupted 是启动清扫（重启打断的 running 行落 failed）。
	SweepInterrupted(ctx context.Context)
}

// BackupPorts 是备份装配器的构造端口袋（组合根在 engine construct.go 绑定
// 核方法值/闭包与 repo；形态与备份应用模块的 Deps 逐字段同形，契约叶子
// 只依赖 domain 类型——应用模块包类型不得穿过本叶子）。
type BackupPorts struct {
	// ObjectStore 是产物承载端口（对象 Put/Get/Delete/List——备份/恢复/
	// 保留/孤儿对账四面）。
	ObjectStore capability.ObjectStore
	// Utility 是 Runtime 工具容器扩展接口（备份/恢复执行载体）。
	Utility capability.RuntimeUtility
	// Repos 是聚合目录（Backups/Databases/Volumes/PubSubs；transact.Stores
	// 整体注入，ADR-0088 决策 3）。
	Repos transact.Stores
	// DB 是快照面（VACUUM INTO）。
	DB state.DB
	// Tx 是手动触发的事务入口（事件 + 审计一拍落账）。
	Tx transact.Tx
	// Options 是域配置快照（Backup 段 + DataRoot；构造后冻结）。
	Options BackupOptions
	// Log 是域内日志器。
	Log *slog.Logger
	// Clock 是域内时间面（节拍/保留窗/时间戳）。
	Clock state.Clock
	// LockMaintenance 是 maintenance 端口读半边（utility 附着项目网与网络
	// 重建串行化，ADR-0046）。
	LockMaintenance func() func()
	// CommitWrite 是 writer 端口（签名单源本叶子）。
	CommitWrite CommitWrite
	// EmitObservation 是 emitter 端口（事件/审计发射）。
	EmitObservation func(ctx context.Context, team, name, aggregate, id string, payload []byte) error
	// EventTeamOfProject 是 emitter 侧 team 解析器。
	EventTeamOfProject func(ctx context.Context, s transact.Stores, projectID string) string
	// ProjectTeamOf 是项目解析器端口（网络命名空间铸造）。
	ProjectTeamOf func(projectID string) string
	// ProjectNetworkFactsNamesOnly 是项目网络名解析端口（utility 附着面）。
	ProjectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	// ControlPlaneNode 是控制面节点解析端口（预置卷钉住）。
	ControlPlaneNode func(ctx context.Context) (string, error)
	// EnsureDatabaseVolume 是卷行确保端口（预置卷恢复）。
	EnsureDatabaseVolume func(ctx context.Context, row *database.Database) (*volume.Volume, error)
	// DatabasePassword 是 database 类型凭证解析端口。
	DatabasePassword func(ctx context.Context, row *database.Database) (string, error)
	// KickDatabases 是收敛循环唤醒端口（preseed 完成后的首启收敛）。
	KickDatabases func()
	// PubSubPassword 是 pubsub 类型凭证解析端口（B4b）。
	PubSubPassword func(ctx context.Context, row *pubsub.PubSub) (string, error)
}

// BackupFactory 由组合根注入（engine.Deps.BackupFactory），把端口袋装配成
// Backup。engine 核不 import 应用模块包；工厂缺席（手动测试装配）时备份面
// 停用：backup 环退化为 no-op 步，门面 nil-safe，componentrestart 跳过
// ObjectStore 读，lifecycle 跳过启动清扫。
type BackupFactory func(BackupPorts) Backup
