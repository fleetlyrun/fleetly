// Package entityid 是平台实体 id 的装配与校验单源（ADR-0070）：id 形如
// "<base>-<seq>"（web-42），seq 是所在表创建序（事务内 MAX+1）——身份
// 消歧（同名重建）、创建序、never-reuse 三机制合一：序号由单写者事务
// 分配、含 tombstone 行取 MAX、构造性永不重发。id 铸成后不可变；id 是
// 不透明字符串，base 段允许连字符（nightly-backup-33），全仓禁止解析
// 其内部结构（归属判定只查平台表，架构 §8）。
package entityid

import (
	"fmt"
	"regexp"
)

// MaxLen 是 id 总长上限（TEXT 主键无实际约束，此界只为派生名/DNS 链条
// 留出可推理的边界；载体名超长由各 Provider 既有截断+哈希兜底）。
const MaxLen = 128

// pattern 是平台实体 id 全形：小写字母数字段以单连字符相连（无首尾
// 连字符、无连续连字符）。
var pattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)+$`)

// namePattern 是 id base 的形态（= 有名实体 name 的 dns 标签形态，
// ADR-0070 决策 4）：字符集安全是 fleetly-<projectID> 命名空间、
// db-<id> DNS 名等派生名的前提。
var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// MaxNameLen 是 base（= 有名实体 name）长度上限：dns 标签惯例 63。
const MaxNameLen = 63

// Join 装配 id：base + "-" + 十进制 seq。base 须先过 ValidName（派生链
// 的 base 是父 id 或 kind 词，天然满足；有名实体由 API 层 name 校验保证）。
func Join(base string, seq int64) string {
	return base + "-" + fmt.Sprintf("%d", seq)
}

// Valid 报告 s 是否为合法平台实体 id 形态。
func Valid(s string) bool {
	return len(s) <= MaxLen && pattern.MatchString(s)
}

// ValidName 报告 s 是否可作为 id base（= 有名实体 name 的合法形态）。
func ValidName(s string) bool {
	return len(s) <= MaxNameLen && namePattern.MatchString(s)
}

// Derive 装配派生实体 id：<父id>-<kind><seq>（web-42-d7）。kind 字母消歧
// 同父兄弟的跨表同号（deployment/build 同以 app 为 base、各自表 seq 独立
// 计数——无 kind 则 web-42-1 双关）。
func Derive(parentID, kind string, seq int64) string {
	return fmt.Sprintf("%s-%s%d", parentID, kind, seq)
}

// Revision 由 app id 与 revision per-App 序号派生确定性 revision id
// （web-42-r7）：per-App 修订号是 UI 面既有语义（r<n>），seq 沿用。
func Revision(appID string, seq int64) string {
	return fmt.Sprintf("%s-r%d", appID, seq)
}

// Audit 由 audit 行 seq 装配审计 id（aud-8492）：审计只增，seq 全局单调
// 即 id 唯一且永不复用。
func Audit(seq int64) string {
	return fmt.Sprintf("aud-%d", seq)
}
