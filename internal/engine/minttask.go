package engine

// one-shot Task 铸造原语（架构评审第二轮候选 2）：schedule 到期拍/手动拍
// 与 firstBoot job 两个调用点的共享骨架收编——此前逐字双份（taskSpawnQuota
// 的 checks 段收编是同族先例，注释在 task.go）。原语拥有不变骨架的全部：
// ULID、TaskRef 注入、protojson 冻结、行形状（Name 空 / Form one-shot /
// 并发 1 / DNS 名）、commitWrite 四件（配额 + tasks.Create + task.created
// + 审计）与 taskLoop Kick。调用方的域差异收敛为三参数：assemble（spec
// 来源——不带 Task 字段，原语注入身份；错误由调用方包装保留域语境）、
// domainSteps（域副作用的 writeStep 构造器，收铸造出的 taskID——记账
// 事件/游标都要锚定新 Task）、actorCtx（task.create 审计是否携带调用方
// 身份——用户动作 true，部署链内的机制动作 false）。
//
// Name 留空的语义随原语继承（终态 Task 永久占名——tasks 唯一索引含终态
// 行；拍出的 Task 靠各域记账导航，不占项目名位）。CAS/冲突语义同单回滚
// 整体继承：域 step 失败 = Task 行不落孤账。F2.2 备份执行器是第三个消费
// 者（ADR-0029 决策 7：定时 → 铸 one-shot 跑模板备份命令）。

import (
	"context"
	"database/sql"
	"fmt"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/state/task"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/encoding/protojson"
)

// mintOneShotTask 铸一条系统级 one-shot Task 并同事务落账（域副作用随
// domainSteps 同单提交）：任一环节失败整单回滚。
func (e *Engine) mintOneShotTask(ctx context.Context, projectID string,
	assemble func() (*specv1.TaskSpec, error),
	domainSteps func(taskID string) []writeStep,
	actorCtx bool,
) (*task.Task, error) {
	spec, err := assemble()
	if err != nil {
		return nil, err
	}
	taskID := ulid.Make().String()
	spec.Task = &specv1.TaskRef{Id: taskID, Project: projectID}
	body, err := protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: true}.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("marshal one-shot task spec: %w", err)
	}
	row := &task.Task{
		ID: taskID, ProjectID: projectID, Name: "",
		Form: task.FormOneShot, State: task.StateActive, Spec: body,
		DesiredConcurrency: 1, NetworkGroup: spec.GetNetworkGroup(),
		DNSName: TaskDNSName(taskID),
	}
	err = e.db.Tx(ctx, func(tx *sql.Tx) error {
		return e.commitWrite(ctx, tx, writeFact{
			// 配额检查（含本单自占一位；ADR-0017 附录 A.1）：命中返回哨兵——
			// 调用方转各自语义（到期拍 skip / firstboot 有界等待 / 手动拍
			// 诚实拒绝）。
			checks: []func(ctx context.Context, tx *sql.Tx) error{
				func(ctx context.Context, tx *sql.Tx) error { return e.taskSpawnQuota(ctx, tx, projectID, 1) },
			},
			write: func(ctx context.Context, tx *sql.Tx) error { return e.tasks.Create(ctx, tx, row) },
			events: []func() eventFact{func() eventFact {
				return eventFact{name: EventTaskCreated, aggregate: "task", id: row.ID, payload: TaskCreatedEventJSON(row)}
			}},
			audits: []auditFact{{action: "task.create", resource: "task/" + row.ID, afterFP: row.Form, actorCtx: actorCtx}},
			steps:  domainSteps(taskID),
		})
	})
	if err != nil {
		return nil, err
	}
	e.taskLoop.Kick() // 新 Task 即刻驱动（补足 Run 不等节拍）
	return row, nil
}
