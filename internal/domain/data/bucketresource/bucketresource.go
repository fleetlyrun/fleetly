// Package bucketresource 是 Bucket 子资源聚合的领域面（B8，ADR-0096 决策
// 10 / design §4.7）：bucket store 实例内的托管 Bucket 声明行——**声明行 +
// admin 工具容器收敛**（silo 镜像自带 mcli：`mcli mb`/`mcli quota set
// --size`/`mcli rb --force`；删除 = 声明式 GC，Bucket 删除即对象数据删除
// ——wide 档确认）。包名取 bucketresource：bucket 一词由 Bucket Store 词条
// 域内本义覆盖（实体组 `buckets` 已被 Bucket Store 实体占用——CLI 避撞律
// 下子资源组名取单数 `bucket`，域包同律分名）。可变面：quota（hard quota，
// live 定案 2026-11-07 silo RELEASE.2026-10-26：`mcli quota set local/<b>
// --size <size>` 与 `mcli quota info` 回读实测在册——v1 全面在册，无挂账）。
// ADR-0075 Repo 接口层：行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/bucketresource。
package bucketresource

// Status 值域（收敛段推进：pending/ready/error——topic.Status 同形注）。
const (
	StatusPending = "pending"
	StatusReady   = "ready"
	StatusError   = "error"
)

// Bucket 是聚合行（软删 tombstone = 声明式 GC 挂起；物理删除成功后收敛
// 段硬删行）。S3 bucket 名值域（≥3 字符等）在受理位校验。
type Bucket struct {
	ID        string
	Seq       int64
	ProjectID string
	// BucketStoreID 是父 bucket store 实例行 id（silo 引擎）。
	BucketStoreID string
	Name          string
	// QuotaBytes 是 hard quota 期望（nil = 无配额；可变面——Update 双档
	// 同律的 bucket 形）。
	QuotaBytes *int64
	Status     string
	StatusErr  string
	CreatedAt  string
	UpdatedAt  string
	DeletedAt  string
}

// Deleted 报告 tombstone 状态（= 声明式 GC 挂起中）。
func (b *Bucket) Deleted() bool { return b.DeletedAt != "" }
