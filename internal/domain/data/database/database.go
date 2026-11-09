// Package database 是 Database 聚合的领域面（CONTEXT.md Database 词条：
// 由模板渲染的托管有状态服务；ADR-0029：模板参数创建即不可变，无
// Revision/Deployment 行——generation 是收敛环的下发单调编号；ADR-0075
// Repo 接口层）。行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/database（连接串材料经 CredentialsRef 指向
// Secret 单真源，驱动机械留实现侧）。
package database

// Status 值域（ADR-0029 决策 9：收敛环从观测缓存推进；kebab 拼写与
// Task/Deployment 状态值同款冻结）。
const (
	StatusPending  = "pending"
	StatusRunning  = "running"
	StatusDegraded = "degraded"
	StatusStopped  = "stopped"
)

// RestoreState 值域（ADR-0039 决策 6 修订：恢复显式终态机，migration
// 00045）。” = 无恢复；pending/running = 在途（备份环枚举与 preseed 门控
// 的活态集）；succeeded/failed = 终态——failed 即终止重试，半恢复态不再
// 静默收敛（restore_from_backup 保留为源历史，不因成功清位）。
const (
	RestoreStateNone      = ""
	RestoreStatePending   = "pending"
	RestoreStateRunning   = "running"
	RestoreStateSucceeded = "succeeded"
	RestoreStateFailed    = "failed"
)

// RestoreActive 报告恢复是否处于在途活态（pending/running——备份环继续
// 推进、preseed 门控与动态重放让位）。终态（succeeded/failed）不再活跃。
func RestoreActive(state string) bool {
	return state == RestoreStatePending || state == RestoreStateRunning
}

// Database 是聚合行（软删 tombstone；挂靠卷与凭证 Secret 是 Project 级
// 材料——卷名 = 数据库名的确定性公式、Secret 名 = credentials_ref，
// 删除不级联）。
type Database struct {
	ID             string
	Seq            int64
	ProjectID      string
	Name           string
	Engine         string
	CredentialsRef string
	// Version 是用户面 major 短名（ADR-0094 决策 1：创建时选定、存续期
	// 不可变；migration 00039 起 NOT NULL 无回填——回显与绑版解析一律
	// 行值直读，退役服务端模板动态取双源）。
	Version string
	// Settings 是白名单旋钮的已受理子集（ADR-0095 决策 1；键 = 引擎原生
	// 词，值域经 dataservicetemplate.ValidateSettings 受理位单源执法）。选
	// map[string]string 而非 JSON 原串：聚合面诚实承载值域结构（nil =
	// 无旋钮 = 现状），列形（TEXT JSON、键序稳定）是持久层细节留在 repo。
	Settings map[string]string
	// CPUMillis/MemoryMB 是载体资源面（ADR-0095 决策 7：nil = 无限制 =
	// 现状；static 语义——变更经载体替换收敛，Update 动词随 B2d）。
	CPUMillis  *int32
	MemoryMB   *int32
	Generation uint64
	// SpecFingerprint 是当前 generation 对应的投影指纹（重启安全：指纹
	// 未变则重放同 gen，不触发载体滚动——ADR-0029 决策 3）。
	SpecFingerprint     string
	BackupIntervalSecs  int64
	BackupRetentionSecs int64
	Status              string
	// LastBackupAt 是最近一次成功 Backup 的完成时刻（定时调度锚；
	// '' = 从未成功，调度锚退回 created_at，migration 00019）。
	LastBackupAt string
	// RestoreFromBackup 是恢复源备份行 id（历史事实：创建时选定，成功/
	// 失败都不清位；在途判定一律读 RestoreState，ADR-0039 决策 6 修订）。
	RestoreFromBackup string
	// RestoreState 是恢复显式状态（migration 00045：''/pending/running/
	// succeeded/failed；failed 是终态——permanent 失败不再重试）。
	RestoreState string
	// RestoreError 是最近一次恢复失败的报文（失败终态留痕；成功不清此列
	// ——历史事实）。
	RestoreError string
	CreatedAt    string
	UpdatedAt    string
	DeletedAt    string
}

// Deleted 报告 tombstone 状态。
func (d *Database) Deleted() bool { return d.DeletedAt != "" }
