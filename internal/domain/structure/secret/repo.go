package secret

import (
	"context"
)

// Repo 是 Secret 聚合存取。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Upsert 写入（同名活跃行覆盖：fingerprint 变更即新值；四件一拍的审计
	// 由调用方同事务组合）。
	Upsert(ctx context.Context, s *Secret) error
	// GetByName 读活跃行（含密文——仅注入/分发路径调用；API 回显面只用
	// ListFingerprints）。
	GetByName(ctx context.Context, projectID, name string) (*Secret, error)
	// ListFingerprints 返回 Project 活跃 Secret 的指纹面（回显契约：值永不
	// 出现）。name 字典序升序 + after 游标（ADR-0026 after_* + limit 惯例
	// ——游标轴 = 既有排序轴 name，分页只动行集不改每行形态）。limit<=0 或
	// >200 回落/钳制缺省 50。
	ListFingerprints(ctx context.Context, projectID, afterName string, limit int) ([]Secret, error)
	// ListAll 读全表行（含 tombstone）：离线维护面（KEK 重封）专用——墓碑
	// 行的密文同样要可解（undelete 路径依赖），含密文列故禁止 API 回显面
	// 消费。
	ListAll(ctx context.Context) ([]Secret, error)
	// UpdateCiphertext 重写密文（KEK 重封专用：fingerprint 是明文的函数，
	// 重封不变；updated_at 随写推进）。行不存在不报错——调用方（rewrap）的
	// 行列表与本写同窗口，无并发删除面。
	UpdateCiphertext(ctx context.Context, id string, ciphertext []byte) error
	// SoftDelete 落 tombstone（幂等）。
	SoftDelete(ctx context.Context, projectID, name string) error
}
