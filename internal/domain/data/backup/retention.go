package backup

// 保留策略判定（P3a 批第 5 单元，ADR-0087 决策 1/2/7：备份保留自 engine/
// infra 侧归域——「给定台账行集 + 行上 retention 快照 → 应删/应留」的纯
// 判定下沉聚合域包；行读（repo）与行/对象删除、环/节拍/事件留 engine 与
// infrastructure〔engine 形态 = 装配（读台账前沿）→ 域裁决（本文件选删
// 集）→ 执行（行/对象成对删）〕。平台轨的快照保留窗表达（restic
// keep-within duration）同批归域。纯度铁律（ADR-0087 决策 2 + domain 三
// 红线）：零 I/O、时间作参数传入（禁 time.Now——domainpurity 纪律项）、
// 不写 receiver、不 import genproto。

import (
	"math"
	"strconv"
	"time"
)

// ---- 数据库轨：台账行保留滚动（ADR-0039 决策 7：行驱动，窗 = created_at + 行上 retention 快照）----

// PruneDue 报告该行是否已过保留窗（保留滚动的单行判定核；窗 = 行创建
// 时刻冻结的 retention_secs 快照——窗在行上逐行不同，SQL 单 cutoff 表达
// 不了，判定逐行做〔原 Go 侧过滤自 repo DueForPrune 平移归域〕）。时间
// 面异常的行不可删（诚实跳过，等人工分诊）；边界 = 严格窗外（elapsed ==
// retention 仍留）。窗判定无状态面（不问 status——在途行老于行级窗理论
// 上可达，R05-L6 登记在案随备份链完整性批另修，本判定忠实承载原口径）。
// 纯函数：不写 receiver，行删与对象清理由调用方成对执行。
func (b *Backup) PruneDue(now time.Time) bool {
	created, err := time.Parse(time.RFC3339, b.CreatedAt)
	if err != nil {
		return false
	}
	return now.Sub(created) > time.Duration(b.RetentionSecs)*time.Second
}

// PruneSelection 从台账前沿行集选出应删行（PruneDue 的集面；engine 的
// 装配→裁决→执行中裁决步）。保序：输入序传导到输出（repo OldestByCreated
// 的 created_at 升序 → 删序稳定）；空选集返回 nil（len==0 同空集）。
func PruneSelection(rows []Backup, now time.Time) []Backup {
	var out []Backup
	for _, b := range rows {
		if b.PruneDue(now) {
			out = append(out, b)
		}
	}
	return out
}

// ---- 平台轨：快照保留窗的 restic 表达（ADR-0039 决策 9：forget --keep-within 双仓同策略）----

// ResticKeepWithin 把保留窗铸成 restic duration（值域单位 y/m/w/d/h——
// 不收 Go duration 串的 m/s 尾巴；dind 实证 "168h0m0s" 被拒）。小时向上
// 取整、最小 1h（亚小时保留窗在 restic 面无法表达，向上取整 = 多留不
// 少留的诚实方向）。
func ResticKeepWithin(retention time.Duration) string {
	hours := int(math.Ceil(retention.Hours()))
	if hours < 1 {
		hours = 1
	}
	return strconv.Itoa(hours) + "h"
}
