package capability

// Workload 期望集签名——幂等收敛跳过判定的 canonical 形态。签名知识随类型
// 走：全集 marshal，手挑字段子集在此位置不可能复发（2026-10-11 前的手挑
// 五字段签名使 EgressNetworks——ADR-0052 投影期现值解析字段——静默掉出
// 跳过判定，ensure memo 对 egress 变更盲；runbook 记录·三十七）。

import (
	"encoding/json"
	"sort"
)

// WorkloadSetSignature 计算期望集签名（消费方 engine.ensureTaskWorkloads）。
// 逐字节稳定：按 Workload ID 排序消除集合序，encoding/json 全 struct
// marshal——字段序=声明序、map 键序内建排序、slice 序=投影序，全部确定性。
//
// 任何新 Workload 字段落类型即落签名。改字段结构 = 存量 memo 值域整体失效
// 一拍（幂等 Ensure 全量重放自愈），无迁移。
func WorkloadSetSignature(ws []Workload) string {
	sorted := append([]Workload(nil), ws...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	b, _ := json.Marshal(sorted) //nolint:errcheck // Workload 是纯数据结构（标量/切片/映射），Marshal 不失败
	return string(b)
}
