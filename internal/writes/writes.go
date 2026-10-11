package writes

// 写序列 kernel（ADR-0064，架构评审第三轮候选 2）：checks → write →
// events → audits 的唯一序列拥有者。此前 API 受理位 commit（acceptance.go）
// 与 engine 驱动域 commitWrite（transition.go）各持一份同构循环——2026-10-03
// 的漂移史（五份手搓拷贝中 task 域拷贝"注释承诺审计、代码无审计"）证明
// 序列各自演化是真实失败模式；本 kernel 收编后两层只做各自 writeFact 形态
// 的归一化。
//
// 分层：叶子包，只依赖 state/outbox 与 state/audit（两侧落库句柄同形，
// 直收具体 repo 不设接口抽象）。状态迁移四件一（CAS + 原地迁移抑制事件）
// 不在此——那是 engine 自有的 transition 语义（transition.go 单源），本
// kernel 只承载非迁移写序列。steps 域差异段由 engine commitWrite 编排、
// 逐段以空 Checks 形重放同一序列。
//
// 事务边界双形态由调用方承载：API commit 自开事务（s.DB.Tx），engine
// commitWrite 收调用方事务（组合进更大事务）——Sequence 只管调用方事务内
// 的段序与段间失败语义（任一段失败即返回，后续段零执行，整单回滚）。

import (
	"context"
	"database/sql"

	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
)

// Event 是一条 Outbox 事件事实（与聚合写同生共死）。
type Event struct {
	Name      string
	Aggregate string
	ID        string
	Payload   []byte
}

// Fact 是一次非迁移写路径的归一化形态；零值段合法。Events/Audits 取 lazy
// 形：事件负载与审计字段可依赖事务内写的返回行（Write 段先于其求值）。
// Audits 组形——一次求值可返 0/N 条（API auditsFrom 条件审计、engine
// actorCtx 铸型各归一为组）。
type Fact struct {
	Checks []func(context.Context, *sql.Tx) error
	Write  func(context.Context, *sql.Tx) error
	Events []func() Event
	Audits []func(context.Context) []*audit.Entry
}

// Sequence 在调用方事务内落完一次写路径：检查先行（拒绝零副作用）、聚合
// 写居中、事件与审计殿后。任一段失败即返回，后续段零执行（调用方事务
// 整单回滚）。
func Sequence(ctx context.Context, tx *sql.Tx, outbox *outbox.Repo, audits *audit.Repo, f Fact) error {
	for _, check := range f.Checks {
		if err := check(ctx, tx); err != nil {
			return err
		}
	}
	if f.Write != nil {
		if err := f.Write(ctx, tx); err != nil {
			return err
		}
	}
	for _, ev := range f.Events {
		e := ev()
		if _, err := outbox.Append(ctx, tx, e.Name, e.Aggregate, e.ID, e.Payload); err != nil {
			return err
		}
	}
	for _, group := range f.Audits {
		for _, entry := range group(ctx) {
			if err := audits.Append(ctx, tx, entry); err != nil {
				return err
			}
		}
	}
	return nil
}
