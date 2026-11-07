// Package bucketstore 是 Bucket Store 聚合的领域面（ADR-0096 决策 1：三聚合
// 三表三 RPC 面不变——bucket_stores 表与 pubsubs 同形（含备份三列/恢复两列
// 的同形缺省：silo 转储挂账〔design §4.5〕，列在而不用——DataCapabilities.
// Backup=false 门控 backups 卫星与受理面，行面同形是 descriptor 归一前提）；
// CONTEXT.md Bucket Store 词条：由模板渲染的 S3 兼容对象存储托管服务；
// ADR-0029 第三形态同律——模板参数创建即不可变，generation 是收敛环的下发
// 单调编号；ADR-0075 Repo 接口层）。行类型与 Repo 接口在此单源，SQLite 实现
// 住 infrastructure/persistence/bucketstore。
package bucketstore

// Status 值域（与 Database/PubSub 同形冻结：pending/running/degraded/
// stopped，收敛环从观测缓存推进）。
const (
	StatusPending  = "pending"
	StatusRunning  = "running"
	StatusDegraded = "degraded"
	StatusStopped  = "stopped"
)

// BucketStore 是聚合行（软删 tombstone；挂靠卷与凭证 Secret 是 Project 级
// 材料——卷名 = 实例名的确定性公式、Secret 名 = credentials_ref，删除不
// 级联）。字段面与 pubsub.PubSub 同形（design §4.1 聚合与表）。
type BucketStore struct {
	ID             string
	Seq            int64
	ProjectID      string
	Name           string
	Engine         string
	CredentialsRef string
	// Version 是用户面 major 短名（ADR-0094 决策 1：创建时选定、存续期
	// 不可变；silo 单版 2026-10）。
	Version string
	// Settings 是白名单旋钮的已受理子集（ADR-0095 决策 1；键 = 引擎原生
	// 词——silo v1 零旋钮，恒空集）。
	Settings map[string]string
	// CPUMillis/MemoryMB 是载体资源面（nil = 无限制 = 现状；static 语义）。
	CPUMillis  *int32
	MemoryMB   *int32
	Generation uint64
	// SpecFingerprint 是当前 generation 对应的投影指纹（重启安全；silo 的
	// root SK 住 Workload env——轮换=滚动即指纹变→Generation 推进）。
	SpecFingerprint     string
	BackupIntervalSecs  int64
	BackupRetentionSecs int64
	Status              string
	// LastBackupAt 是最近一次成功 Backup 的完成时刻（定时调度锚；本族
	// Backup=false 恒零值——列在而不用）。
	LastBackupAt string
	// RestoreFromBackup 在场 = 恢复挂起（本族无备份面恒空——列在而不用）。
	RestoreFromBackup string
	// RestoreError 是最近一次恢复失败的报文（同上恒空）。
	RestoreError string
	CreatedAt    string
	UpdatedAt    string
	DeletedAt    string
}

// Deleted 报告 tombstone 状态。
func (b *BucketStore) Deleted() bool { return b.DeletedAt != "" }
