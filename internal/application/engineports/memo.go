// memo.go 是 engineports 的收敛备忘协议契约面（P5 wave-3 随 backups/
// browses 拆包下沉，2026-11-01）：Memo 记录形态与 memo 端口三签名
// （EnsureFresh/EnsureRemember/EnsureForget）在此单源——engine 核（ensure.go
// 的 ensureMu 单锁协议本体）与卫星包（browses 的 memo 表随域持有）同一
// 契约。锁协议本体（单锁 + 表归属）留核：端口是实现面注入，卫星结构性
// 无法绕锁直写（ADR-0088 决策 3/4；metastatewrite D3 的 memo 登记制继续
// 执法表本体）。
package engineports

import (
	"time"
)

// Memo 是收敛域「上次成功 Ensure」的备忘（N1 C16/C17）：Sig 是该域
// Ensure 全部输入的签名字面（域内定义——受管 fingerprint 全量保守、
// Database 行锚、Task 语义等价缩面、Browse 会话投影+材料等），At 驱动
// 周期强制重放（签名未变也周期收敛——自愈面），Gen 备短路拍需要下发
// 编号的消费面（Database 状态推进）。
type Memo struct {
	Sig string
	Gen uint64
	At  time.Time
}

// EnsureFresh 是 memo 协议的读半边：报告键上备忘是否存在且（签名未变 +
// 未到强制重放节拍）——双条件同时成立才允许跳过本拍重活（实现持锁读）。
type EnsureFresh func(memo map[string]Memo, key, sig string, now time.Time) (Memo, bool)

// EnsureRemember 是 memo 协议的写半边：落/刷键上备忘（仅成功路径调用；
// 实现持锁写）。
type EnsureRemember func(memo map[string]Memo, key string, rec Memo)

// EnsureForget 是 memo 协议的清半边：清键上备忘（Ensure 失败/域收口；
// 缺失为 no-op；实现持锁删）。
type EnsureForget func(memo map[string]Memo, key string)
