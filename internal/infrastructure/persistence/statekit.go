// statekit.go 是 persistence 层五件共享机械件（P5-1 statekit 收口）：页宽
// 钳位、SQL 占位串、状态 CAS 前置检查、状态集文案、查询参数铸造与状态
// CAS WHERE 片段。循根包共享机械先例（NextSeq/IsUniqueViolation）住本根
// 包——各聚合 repo 共用，杜绝此前 26 处手写钳位、三份逐字相同的
// placeholders、四份 stateIn 骨架继续复制繁衍。
//
// 纪律：以下六原子是各自语义的「唯一入口」——聚合 repo 不得再手写同语义
// 的本地副本（守卫见 internal/guards/statekitlaw_test.go：G7 页宽钳位律 +
// G8 statekit 机械件律，手搓即红）。
package persistence

import "strings"

// ClampLimit 规范列表页宽：limit 非正或超上界 max 回落 def。列表方法的
// 钳位语义全仓单一（<=0 或 >max → def），站点只声明自己的 (def, max)——
// 手写 `if limit <= 0 || …` 即红（G7 页宽钳位律，唯一入口）。
func ClampLimit(limit, def, max int) int {
	if limit <= 0 || limit > max {
		return def
	}
	return limit
}

// Placeholders 铸 n 个 SQL 占位符 "?,?,…"（IN 子句构建唯一入口）。占位符
// 个数与参数切片长度由调用方对齐（len(states) 惯例），本函数只管形态——
// 手写 strings.Repeat("?,") 拼串即红（G8 statekit 机械件律）。
func Placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// StateIn 报告 s ∈ set（状态 CAS 前置检查）：Transit 先经此判前置，不符
// 归一 ErrConflict——线性扫描，状态集个位数，无表驱动必要。
func StateIn[S ~string](set []S, s S) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// JoinStates 铸状态集人类文案（竖线分隔，与现行 task/deployment/run 三包
// 逐字同形）——CAS 报错文案的 want-one-of 面，逐字节保真是迁移纪律。
func JoinStates[S ~string](set []S) string {
	parts := make([]string, len(set))
	for i, s := range set {
		parts[i] = string(s)
	}
	return strings.Join(parts, "|")
}

// ToAny 铸 []string→[]any 查询参数（database/sql 参数面是 []any，词表
// []string 不可直传）——IN 子句参数的装配原子。
func ToAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// TransitWhere 是状态 CAS UPDATE 的行锚+状态前置 WHERE 片段（四件一拍的
// "状态"件并发防御纵深）：UPDATE 尾缀一律 `…` + persistence.TransitWhere
// 拼接——手写 `WHERE id = ? AND state = ?` 即红（G8，片段漂移即 CAS 语义
// 漂移）。
const TransitWhere = "WHERE id = ? AND state = ?"
