// memo.go 是 engine/contract 的收敛备忘协议契约面（P5 wave-3 随 backups/
// browses 拆包下沉，2026-11-01）：Memo 记录形态与 memo 端口三签名
// （EnsureFresh/EnsureRemember/EnsureForget）在此单源——engine 核（ensure.go
// 的 ensureMu 单锁协议本体）与卫星包（browses 的 memo 表随域持有）同一
// 契约。锁协议本体（单锁 + 表归属）留核：端口是实现面注入，卫星结构性
// 无法绕锁直写（ADR-0088 决策 3/4；metastatewrite D3 的 memo 登记制继续
// 执法表本体）。
package contract

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

// dynamic 旋钮热应用的 per-entity 备忘端口（B2d，ADR-0095 决策 6「dynamic
// memo 协议」）：AppliedDigest 记录「已成功热应用的 dynamic 子集摘要」，
// Update 路径在热应用成功后、行落库同窗口经本端口写入（禁直写卫星私有
// 表，ADR-0088 决策 7——与 Memo 三签名同构：表本体在组合根铸造注入、锁
// 协议留核）；**失败路径强制 Forget**（半应用黑洞修：行与 memo 俱不动
// 会让 memo 永不失配、引擎停半应用态——多旋钮部分失败时环重放拉回行值
// 收敛全靠 Forget 打开失配口）。表形态 map[string]string（databaseID →
// 摘要）——比 Memo 轻半边：无节拍/无 gen（重放触发判据只有失配一维，
// 仅 status=running 门由环侧承担）。
type (
	// AppliedRemember 是热应用 memo 的写半边（热应用成功后、行落库同窗口
	// 调用；实现持锁写）。
	AppliedRemember func(memo map[string]string, key, digest string)
	// AppliedForget 是热应用 memo 的清半边（热应用失败路径强制调用——
	// 失配拉回语义的开口；缺失为 no-op；实现持锁删）。
	AppliedForget func(memo map[string]string, key string)
	// AppliedDigestOf 是热应用 memo 的读半边（环重放门的失配判据输入；
	// 实现持锁读）。
	AppliedDigestOf func(memo map[string]string, key string) (string, bool)
)
