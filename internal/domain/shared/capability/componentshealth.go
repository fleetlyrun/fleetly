// componentshealth.go 是组件健康快照的域值类型（B2b 共享导出面）：探活
// 聚合器（engine 持 Providers 集）的唯一产出形态，systemgrpc status 面与
// diagnosis 事故装配双消费——proto 映射留 interfaces 层。
package capability

import "time"

// ComponentHealth 是单个组件的探活快照（provider.Describe 身份 + Health
// 结果；Details 携上游错误原文——platform 面（ListPlatformIncidents）可
// 转写，App 诊断 evidence 只链组件名不转写放大）。
type ComponentHealth struct {
	// Name 是组件名（provider.Describe().Name）。
	Name string
	// Capability 是能力种类（capability.Kind 词面）。
	Capability string
	// Healthy 是探活结果（超时/panic 按不健康计——降级矩阵诚实口径）。
	Healthy bool
	// Details 是探活详情（上游错误原文；健康时通常空）。
	Details string
	// CheckedAt 是本轮探活时刻（快照窗口锚）。
	CheckedAt time.Time
}

// UnhealthyNames 返回不健康组件名集（升序——事故装配与断言的确定性锚）。
func UnhealthyNames(cs []ComponentHealth) []string {
	out := make([]string, 0, 2)
	for _, c := range cs {
		if !c.Healthy && c.Name != "" {
			out = append(out, c.Name)
		}
	}
	for i := 1; i < len(out); i++ { // 插入排序——集合小，零依赖稳定序
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
