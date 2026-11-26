// memo.go 是 engine/contract 的收敛确保缓存协议契约面（P5 wave-3 随 backups/
// browses 拆包下沉，2026-11-01；批 1 句柄化改接口形态，2026-11-25）：Memo
// 记录形态在此单源，协议消费面从「函数类型 + 裸表传参」改为句柄接口——
// 表本体与互斥量未导出于实现（engine 核），影子表与绕锁直写结构性不可能
// （memoMapRegistry 登记制随之退役）。锁协议本体（单锁）留核：所有句柄
// 共享引擎 ensureMu，锁拓扑零漂移。
package contract

import (
	"time"
)

// Memo 是收敛域「上次成功 Ensure」的确保缓存（N1 C16/C17）：Sig 是该域
// Ensure 全部输入的签名字面（域内定义——受管 fingerprint 全量保守、
// Database 行锚、Task 语义等价缩面、Browse 会话投影+材料等），At 驱动
// 周期强制重放（签名未变也周期收敛——自愈面），Gen 备短路拍需要下发
// 编号的消费面（Database 状态推进）。
type Memo struct {
	Sig string
	Gen uint64
	At  time.Time
}

// EnsureCache 是一张确保缓存表的句柄（批 1 句柄化，§3.5）：一张表一个句
// 柄，表本体与互斥量未导出于实现。
type EnsureCache interface {
	// Fresh 是读半边：报告键上确保缓存是否存在且（签名未变 + 未到强制重
	// 放节拍）——双条件同时成立才允许跳过本拍重活（实现持锁读）。
	Fresh(key, sig string, now time.Time) (Memo, bool)
	// Remember 是写半边：落/刷键上确保缓存（仅成功路径调用；实现持锁写）。
	Remember(key string, rec Memo)
	// Forget 是清半边：清键上确保缓存（Ensure 失败/域收口；缺失为 no-op；
	// 实现持锁删）。
	Forget(key string)
}

// EnsureCaches 是确保缓存句柄的铸造面：互斥量在所有句柄间共享（单锁拓扑
// 不变——引擎 ensureMu 原配对裁定维持）；NewAppliedCache 铸造 dynamic 热
// 应用轻半边表（B2d，ADR-0095 决策 6）。
type EnsureCaches interface {
	NewEnsureCache() EnsureCache
	NewAppliedCache() AppliedCache
}

// AppliedCache 是 dynamic 旋钮热应用确保缓存表的句柄（B2d，ADR-0095 决策
// 6「dynamic ensure cache 协议」；批 1 句柄化同构）：AppliedDigest 记录
// 「已成功热应用的 dynamic 子集摘要」，Update 路径在热应用成功后、行落库
// 同窗口经本句柄写入；**失败路径强制 Forget**（半应用黑洞修：行与缓存俱
// 不动会让缓存永不失配、引擎停半应用态——多旋钮部分失败时循环重放拉回
// 行值收敛全靠 Forget 打开失配口）。表形态 map[string]string（databaseID
// → 摘要）——比 Memo 轻半边：无节拍/无 gen（重放触发判据只有失配一维，
// 仅 status=running 门由循环侧承担）。
type AppliedCache interface {
	// Remember 是写半边（热应用成功后、行落库同窗口调用；实现持锁写）。
	Remember(key, digest string)
	// Forget 是清半边（热应用失败路径强制调用——失配拉回语义的开口；缺
	// 失为 no-op；实现持锁删）。
	Forget(key string)
	// DigestOf 是读半边（循环重放门的失配判据输入；实现持锁读）。
	DigestOf(key string) (string, bool)
}
