// Package pubsub 是 PubSub 聚合的领域面（ADR-0096 决策 1：三聚合三表三
// RPC 面不变——pubsubs 表与 databases 同形，含 ADR-0094 version 列、
// ADR-0095 settings/resources 列与 Update 动词；CONTEXT.md PubSub 词条：
// 由模板渲染的托管消息服务；ADR-0029 第三形态同律——模板参数创建即不可
// 变，generation 是收敛环的下发单调编号；ADR-0075 Repo 接口层）。行类型
// 与 Repo 接口在此单源，SQLite 实现住 infrastructure/persistence/pubsub。
package pubsub

// Status 值域（与 Database 同形冻结：pending/running/degraded/stopped，
// 收敛环从观测缓存推进）。
const (
	StatusPending  = "pending"
	StatusRunning  = "running"
	StatusDegraded = "degraded"
	StatusStopped  = "stopped"
)

// PubSub 是聚合行（软删 tombstone；挂靠卷与凭证 Secret 是 Project 级材料
// ——卷名 = 实例名的确定性公式、Secret 名 = credentials_ref，删除不级联）。
// 字段面与 database.Database 同形（design §4.1 聚合与表）。
type PubSub struct {
	ID             string
	Seq            int64
	ProjectID      string
	Name           string
	Engine         string
	CredentialsRef string
	// Version 是用户面 major 短名（ADR-0094 决策 1：创建时选定、存续期
	// 不可变）。
	Version string
	// Settings 是白名单旋钮的已受理子集（ADR-0095 决策 1；键 = 引擎原生
	// 词，pubtemplate 白名单）。
	Settings map[string]string
	// CPUMillis/MemoryMB 是载体资源面（nil = 无限制 = 现状；static 语义）。
	CPUMillis  *int32
	MemoryMB   *int32
	Generation uint64
	// SpecFingerprint 是当前 generation 对应的投影指纹（重启安全）。
	SpecFingerprint     string
	BackupIntervalSecs  int64
	BackupRetentionSecs int64
	Status              string
	// LastBackupAt 是最近一次成功 Backup 的完成时刻（定时调度锚）。
	LastBackupAt string
	// RestoreFromBackup 在场 = 恢复挂起（备份环执行；ADR-0039 同律）。
	RestoreFromBackup string
	// RestoreError 是最近一次恢复失败的报文（历史事实留痕）。
	RestoreError string
	CreatedAt    string
	UpdatedAt    string
	DeletedAt    string
}

// Deleted 报告 tombstone 状态。
func (p *PubSub) Deleted() bool { return p.DeletedAt != "" }
