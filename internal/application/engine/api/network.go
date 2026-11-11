package api

// 网络重建输入面 DTO 与哨兵（真源自 engine 根包迁出，P3-7 输入面契约
// 收口）：网络重建是平台中介的受监督动词（ADR-0046），响应面直读计数与
// 无法归属附着的拒绝形态在此单源。

import "strings"

// NetworkRebuildResult 是一次重建执行的载体计数（响应面直读）。
type NetworkRebuildResult struct {
	Detached   int
	Reattached int
}

// ForeignAttachmentError 列出平台无法归属的网络附着载体（受理拒绝面：
// 无 fleetly 域标签的外来 service，或域锚不在期望集的残留载体）。API 层
// 映射 E_CONFLICT——诚实拒绝，不碰不认识的载体（WorkloadOrphaned 只登记
// 原则的网络面同款）。
type ForeignAttachmentError struct {
	Carriers []string
}

func (e *ForeignAttachmentError) Error() string {
	return "network has attachments the platform cannot attribute: " + strings.Join(e.Carriers, ", ")
}
