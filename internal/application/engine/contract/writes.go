// writes.go 是 engine/contract 的写路径契约面（P5 wave-3 随 backups/browses
// 拆包下沉，2026-11-01）：非迁移写的声明类型（Check/WriteStep/WriteFact）
// 与 writer 端口签名（CommitWrite）在此单源——engine 核（transition.go 的
// 序列真源 commitWrite/受理前门 Commit 与 api 受理位）经包内别名零漂移，
// 卫星侧（backups 的 TriggerPlatformBackup 审计落账）直接消费注入端口。
// 序列本体（检查 → 写 → 事实扇出 → 附加步）仍单点拥有在 engine 核
// （ADR-0087 决策 4：执行双点、规则单源、原语单份）——本包只持契约形态。
package contract

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
)

// Check 是一条写路径的受理检查：事务内运行，非 nil 错误即整单回滚。
// api 受理位与 engine 非迁移写共用本类型——受理检查的执行作用域由写
// 原语拥有（ADR-0087 决策 4「事务作用域约束」：受理读不退回事务外）。
type Check func(ctx context.Context, s transact.Stores) error

// WriteStep 是事务内的一段附加写（字段 CAS/派生建行）与其伴生事实；
// 序列与主写一致（写 → 事实扇出）。
type WriteStep struct {
	Write func(ctx context.Context, s transact.Stores) error
	Facts []func() writes.FactRow
}

// WriteFact 声明一次写路径的全部事实；零值段合法。Checks 先行（拒绝
// 零副作用）；Facts 在 Write 之后求值（载荷/指纹可依赖事务内写的行）
// ——单声明 kernel FactRow（默认双真）。
type WriteFact struct {
	Checks []Check
	Write  func(ctx context.Context, s transact.Stores) error
	Facts  []func() writes.FactRow
	Steps  []WriteStep
}

// CommitWrite 是 writer 端口的签名（engine 核 commitWrite 方法 = 唯一
// 生产实现：调用方事务内落完一次写路径——检查 → 写 → 事实扇出，附加
// 写步殿后）。P5 wave-3 随 backups 拆包下沉——手动触发面的审计/事件一
// 拍落账经本端口注入卫星（构造面绑定核方法值；单写发射面留核，
// ADR-0088 决策 3）。
type CommitWrite func(ctx context.Context, s transact.Stores, f WriteFact) error
