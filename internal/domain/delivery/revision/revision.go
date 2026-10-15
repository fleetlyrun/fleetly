// Package revision 是 Revision 聚合的领域面（CONTEXT.md Revision 词条：冻结
// 且不可变的规范化 Spec 快照，回滚与审计的单位）。存储形态 = blob 落库
// 裁决（2026-09-30）：spec 列存 AppSpec 的 protojson 规范序列化，digest 列
// 存其 sha256——Platform Backup = 单 SQLite 文件 + 密钥即全部，无第二棵
// 状态树。不可变：无 UPDATE 路径，只有 INSERT 与读。行类型与 Repo 接口
// 在此单源，SQLite 实现住 infrastructure/persistence/revision（ADR-0075
// Repo 接口层路线）。
package revision

import (
	"crypto/sha256"
	"encoding/hex"
)

// Revision 是聚合行（不可变）。SourceText 是受理原文（compose 源的用户
// 亲笔文件；其他源空串——ADR-0074；不参与 digest，见 00031 迁移注记）。
type Revision struct {
	ID         string
	AppID      string
	Seq        int64
	Digest     string
	Spec       []byte
	SourceText string
	CreatedAt  string
}

// Digest 计算 spec 规范序列化的冻结指纹（sha256 hex）。
func Digest(spec []byte) string {
	sum := sha256.Sum256(spec)
	return hex.EncodeToString(sum[:])
}
