// Package sourceupload 是上传产物聚合的领域面（ADR-0019 附录 A，F1.10，
// ADR-0075 Repo 接口层）：行按 (project_id, digest) 唯一——同项目同内容
// 重传返回同一行；blob 是全局内容寻址文件（DataRoot/uploads/<sha256hex>，
// internal/upload 管辖），跨项目行共享同一 blob，refcount=行数。行类型
// 与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/sourceupload。
package sourceupload

// Upload 是上传产物行（无状态机——上传是一次性动作，流中断=tmp 丢弃
// 无行）。
type Upload struct {
	ID        string
	Seq       int64
	ProjectID string
	Digest    string // sha256 hex
	SizeBytes int64
	CreatedAt string
}
