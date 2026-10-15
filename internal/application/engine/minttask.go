package engine

// Task 行铸造单源（ADR-0062，架构评审第三轮候选 1）：身份注入（TaskRef +
// 属主 Token）→ 行缺省（Form 推导、one-shot 缺省并发）→ protojson 冻结 →
// 行装配，只此一处。调用方（API 受理位、域铸造壳 mintOneShotTask）只供组
// 装好的 TaskSpec 与身份字段，行形状知识（Form 推导、one-shot 缺省并发、
// DNS 铸名、冻结选项）不得在 module 之外重现（internal/guards 行铸造反扫
// 执法）。铸造产行，落账仍走各自写路径（受理位 commit 原语 / engine 写序
// 列）——ADR-0024 受理纪律不因铸造单源而变。
//
// spec 验证不住铸造（2026-10-11 cmd golden 实证回退）：铸造输入的效性契约
// 不统一——firstBoot 铸造 spec 是平台投影，携带 taskGroup:/project: 跨挂
// networks，而 ValidateTask 的"一条轨"禁用只治用户提交面（ADR-0012 裁决
// 面）；ValidateTask 还要求 TaskRef 在场，而身份锚恰由铸造注入。验证归各
// 入口受理面：API = normalizeCreateTask（ValidateTaskTemplate，ref 缺席同
// 款规则）、schedule = 模板冻结时 ValidateTaskTemplate、firstBoot = AppSpec
// 受理位 CheckFirstBootNetworks。铸造只保证注入构造出的 ref 非空。
//
// 错误契约：冻结 marshal 失败原样上抛，调用方自加语境。engine 不依赖
// apperr（engine.go 哨兵纪律）。
//
// 冻结选项（snake_case / 不带未填充字段）与受理位 AppSpec 的 marshalSpec
// 同口径：TaskSpec 冻结体是行内唯一期望状态载体，改序列化形态 = 存量行
// 读回漂移，只在可承受一次性数据迁移的批次变更。
//
// one-shot 缺省并发 1 随铸造收编（firstBoot 铸造 spec 不带 Form/Desired、
// schedule 模板可声明 0）：行值此前由各调用方手补（壳硬编码 1、受理位归一
// 化补 1），现在铸造单源负责——只缺省行值，不改写冻结体；配额检查的
// delta 仍由调用方显式传（taskSpawnQuota），不读行值。
//
// mintOneShotTask 是系统级 one-shot 的域铸造壳：配额 check + tasks.Create +
// task.created + 审计四件走 engine 写序列（commitWrite），域副作用随
// domainSteps 同单提交。调用方的域差异收敛为三参数（assemble / domainSteps /
// actorCtx），行形状不再入壳。F2.2 备份执行器是第三个消费者（ADR-0029
// 决策 7：定时 → 铸 one-shot 跑模板备份命令）。
//
// Name 留空的语义随壳继承（终态 Task 永久占名——tasks 唯一索引含终态行；
// 拍出的 Task 靠各域记账导航，不占项目名位）。CAS/冲突语义同单回滚整体
// 继承：域 step 失败 = Task 行不落孤账。

import (
	"context"
	"database/sql"
	"fmt"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"google.golang.org/protobuf/encoding/protojson"
)

// MintTaskRow 铸一条 Task 行：TaskRef 与属主注入调用方 spec（原地变更）、
// 行缺省、冻结并装配行。name 允许空（系统级铸造不占项目名位，base = task）；
// ownerTokenID 允许空（匿名/机制铸造无属主，resident 无租约保温）。seq 由
// 调用方在其写事务内取（e.db.NextSeq 契约方法）——id = <name|task>-<seq>，身份注
// 入先于冻结（ADR-0070；CONTEXT.md Task Mint 词条：铸造单源不动）。
func MintTaskRow(projectID, name, ownerTokenID string, s *specv1.TaskSpec, seq int64) (*task.Task, error) {
	if s == nil {
		return nil, fmt.Errorf("mint task row: nil spec")
	}
	base := name
	if base == "" {
		base = "task"
	}
	taskID := entityid.Join(base, seq)
	s.Task = &specv1.TaskRef{Id: taskID, Project: projectID}
	s.OwnerToken = ownerTokenID
	form := spec.TaskForm(s)
	desired := s.GetDesiredConcurrency()
	if desired == 0 && form == spec.FormOneShot {
		desired = 1 // one-shot 缺省 1（受理位同款规则收编入铸造）
	}
	body, err := protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: true}.Marshal(s)
	if err != nil {
		return nil, err
	}
	return &task.Task{
		ID: taskID, Seq: seq, ProjectID: projectID, Name: name,
		Form: form, State: task.StateActive, Spec: body,
		OwnerTokenID: ownerTokenID, DesiredConcurrency: desired,
		NetworkGroup: s.GetNetworkGroup(), DNSName: TaskDNSName(taskID),
	}, nil
}

// mintOneShotTask 铸一条系统级 one-shot Task 并同事务落账（域副作用随
// domainSteps 同单提交）：任一环节失败整单回滚。行形状经 MintTaskRow 单源，
// 本壳只保留域差异（配额 delta、写序列四件、Kick）。
func (e *Engine) mintOneShotTask(ctx context.Context, projectID string,
	assemble func() (*specv1.TaskSpec, error),
	domainSteps func(taskID string) []writeStep,
	actorCtx bool,
) (*task.Task, error) {
	assembled, err := assemble()
	if err != nil {
		return nil, err
	}
	// 资源轴 Team 先行解析（P0-2）：Project 的 Team 归属不可变，事务外
	// 解析无 TOCTOU 面。
	team := e.eventTeamOfProject(ctx, e.db.Runner(), projectID)
	var row *task.Task
	err = e.db.Tx(ctx, func(tx *sql.Tx) error {
		// 配额检查先行（拒绝零副作用；含本单自占一位；ADR-0017 附录 A.1）：
		// 命中返回哨兵——调用方转各自语义（到期拍 skip / firstboot 有界等待 /
		// 手动拍诚实拒绝）。随后事务内取 seq 铸行（ADR-0070）。
		if err := e.taskSpawnQuota(ctx, tx, projectID, 1); err != nil {
			return err
		}
		seq, err := e.db.NextSeq(ctx, tx, "tasks")
		if err != nil {
			return err
		}
		row, err = MintTaskRow(projectID, "", "", assembled, seq)
		if err != nil {
			return err
		}
		return e.commitWrite(ctx, tx, writeFact{
			write: func(ctx context.Context, tx *sql.Tx) error { return e.tasks.Create(ctx, tx, row) },
			facts: []func() writes.FactRow{func() writes.FactRow {
				fr := writes.FactRow{
					Name: EventTaskCreated, Aggregate: "task", ID: row.ID,
					Payload: TaskCreatedEventJSON(row), TeamID: team,
					Action: "task.create", Resource: "task/" + row.ID, AfterFP: row.Form,
				}
				if actorCtx {
					// actor/source 取请求 ctx——用户动词路径（默认 SourceSystem
					// 由 kernel 缺省铸型）。
					fr.Actor, fr.Source = authn.ActorFromContext(ctx), authn.SourceFromContext(ctx)
				}
				return fr
			}},
			steps: domainSteps(row.ID),
		})
	})
	if err != nil {
		return nil, err
	}
	e.taskLoop.Kick() // 新 Task 即刻驱动（补足 Run 不等节拍）
	return row, nil
}
