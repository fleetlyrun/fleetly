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
	Generation     uint64
	// SpecFingerprint 是当前 generation 对应的投影指纹（重启安全：指纹
	// 未变则重放同 gen，不触发载体滚动——ADR-0029 决策 3）。
	SpecFingerprint     string
	BackupIntervalSecs  int64
	BackupRetentionSecs int64
	Status              string
	// LastBackupAt 是最近一次成功 Backup 的完成时刻（定时调度锚；
	// '' = 从未成功，调度锚退回 created_at，migration 00019）。
	LastBackupAt string
	// RestoreFromBackup 在场 = 恢复挂起（备份环执行；ADR-0039 决策 6）。
	RestoreFromBackup string
	// RestoreError 是最近一次恢复失败的报文（成功恢复不清此列——历史
	// 事实；挂起列清位才是"无恢复在途"的判定锚）。
	RestoreError string
	CreatedAt    string
	UpdatedAt    string
	DeletedAt    string
}

// Deleted 报告 tombstone 状态。
func (d *Database) Deleted() bool { return d.DeletedAt != "" }
