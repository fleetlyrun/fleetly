package capability

// 可选子面的协商点（2026-10-03 架构评审候选 4，grilling 共识三项：
// 包级探测函数 / 探测+断言点替换+可观测面同批 / 启动日志本批 API 后续）。
//
// 此前 duck-typing 发现散在 8 处类型断言点（drift/hygiene/managed×3/
// edgeconfig/contexts×2），降级文化（谁缺谁让位、缺面时各消费点的诚实
// 行为）只活在各点注释里、无处枚举——FacesOf 收进单点，ProviderFaces
// 持 typed 值（nil = 未提供），Offered 给装配期日志与后续 doctor 面
//（GetStatus 扩字段随 F0.19 能力降级矩阵批次设计，避免两次 API 变更）。

// ProviderFaces 是一次 FacesOf 探测的产品：可选子面的 typed 持有。
type ProviderFaces struct {
	// Runtime 子面
	Logs      RuntimeLogs      // 容器日志流（StreamLogs）
	Admin     RuntimeAdmin     // 节点管理（Drain/Cordon 等）
	Inspector RuntimeInspector // spec 对照 drift（ADR-0022）
	Hygiene   RuntimeHygiene   // 孤儿载体清扫（E29-1）
	Utility   RuntimeUtility   // 一次性工具容器执行（ADR-0039 备份执行链）
	// NetworkMaintenance 是载体网络重建原语（ADR-0046 网络重建动词）。
	NetworkMaintenance RuntimeNetworkMaintenance
	// 跨 Capability 子面（受管自宿 ADR-0004 与其材料/配置源）
	Managed         Managed         // 受管部署声明
	MaterialsSource MaterialsSource // 受管域材料集
	ConfigSource    ConfigSource    // 全量动态配置快照
	// Registry 子面（ADR-0036 N2 兑现节 2：per-Project 凭证域隔离）
	ProjectEndpoints       ProjectEndpoints       // per-Project 端点/凭证
	ProjectScopedMaterials ProjectScopedMaterials // 材料随活跃 Project 集再生成
}

// FacesOf 探测一个 Provider 的可选子面。duck-typing 单点：实现即拥有
// （Provider 实现处的编译期断言钉死实现清单，本函数只做运行期枚举）；
// 调用方以 nil-check 分支，降级行为就近自明。
func FacesOf(p Provider) ProviderFaces {
	f := ProviderFaces{}
	if v, ok := p.(RuntimeLogs); ok {
		f.Logs = v
	}
	if v, ok := p.(RuntimeAdmin); ok {
		f.Admin = v
	}
	if v, ok := p.(RuntimeInspector); ok {
		f.Inspector = v
	}
	if v, ok := p.(RuntimeHygiene); ok {
		f.Hygiene = v
	}
	if v, ok := p.(RuntimeUtility); ok {
		f.Utility = v
	}
	if v, ok := p.(RuntimeNetworkMaintenance); ok {
		f.NetworkMaintenance = v
	}
	if v, ok := p.(Managed); ok {
		f.Managed = v
	}
	if v, ok := p.(MaterialsSource); ok {
		f.MaterialsSource = v
	}
	if v, ok := p.(ConfigSource); ok {
		f.ConfigSource = v
	}
	if v, ok := p.(ProjectEndpoints); ok {
		f.ProjectEndpoints = v
	}
	if v, ok := p.(ProjectScopedMaterials); ok {
		f.ProjectScopedMaterials = v
	}
	return f
}

// Offered 返回已提供的面名（稳定序：Runtime 子面在前，跨 Capability
// 子面殿后；装配期日志/诊断枚举面）。
func (f ProviderFaces) Offered() []string {
	out := make([]string, 0, 8)
	if f.Logs != nil {
		out = append(out, "logs")
	}
	if f.Admin != nil {
		out = append(out, "admin")
	}
	if f.Inspector != nil {
		out = append(out, "inspector")
	}
	if f.Hygiene != nil {
		out = append(out, "hygiene")
	}
	if f.Utility != nil {
		out = append(out, "utility")
	}
	if f.NetworkMaintenance != nil {
		out = append(out, "network-maintenance")
	}
	if f.Managed != nil {
		out = append(out, "managed")
	}
	if f.MaterialsSource != nil {
		out = append(out, "materials")
	}
	if f.ConfigSource != nil {
		out = append(out, "config")
	}
	if f.ProjectEndpoints != nil {
		out = append(out, "project-endpoints")
	}
	if f.ProjectScopedMaterials != nil {
		out = append(out, "project-materials")
	}
	return out
}
