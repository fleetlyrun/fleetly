package engine

// preDeployJobs 部署链接线（F1.13，ADR-0030）：releasing 态前半（jobs 子
// 相位）串行执行部署期 init job——job i 成功才铸 job i+1，全部成功才进
// carrier 子相位（L1/L2/L3 不变）。
//
// 执行体 = 一次性 Run 机制（F1.5/F1.6，词汇裁决 ADR-0007：部署期 init job
// 是 Task 的部署期特例）：铸造 one-shot Task（schedule 先例——无名、无
// Owner Lease、系统属主），此后补足/观测/TTL/终态镜像全走 taskStep 既有
// 链。部署链只拥有"何时铸 + 等到何时"。
//
// 持久锚 = deployments.pre_deploy 游标（'' 未开始 | '<idx>:<taskID>' 等待中
// | 'done' 全部完成）：铸造与游标推进同事务，重启重放零重铸；observe_deadline
// 在本子相位承载 job 等待截止（铸造时刻 + ttl + 余量，ADR-0018 墙钟续算），
// 游标是两子相位共用该列的消歧真源。

import (
	"context"
	"errors"
	"fmt"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// drivePreDeployJobs 推进 jobs 子相位一轮：返回 done=true 表示全部完成
// （调用方进 carrier 子相位）。失败不在此收口——返回 error 由 release 统一
// failDeployment（错误文本点名 job 与原因）。Task 配额命中（ADR-0017 附录
// A.1）转有界等待：deadline 收口，不立即失败（配额是暂态压力）。
func (e *Engine) drivePreDeployJobs(ctx context.Context, d *deployment.Deployment, spec *specv1.AppSpec) (bool, error) {
	jobs := spec.GetPreDeployJobs()
	if len(jobs) == 0 && d.PreDeploy == "" {
		return true, nil // 无 job 的 spec 不经过子相位（存量行为零变化）
	}
	idx, taskID, anchored := d.PreDeployAnchor()
	if !anchored {
		if d.PreDeploy == deployment.PreDeployDone {
			return true, nil // carrier 子相位（重启重放入口）
		}
		// 游标空：铸第 0 个 job。配额受阻时也落等待截止（本作业全预算：
		// ttl + 余量），否则首铸永不成功会变成无界重试。
		if _, err := e.mintPreDeployJob(ctx, d, spec, jobs, 0); err != nil {
			if errors.Is(err, ErrTaskQuota) {
				e.log.Error("pre-deploy: mint blocked by task quota, retrying until deadline", "deployment", d.ID, "err", err)
				return e.preDeployBoundedWait(ctx, d, jobs[0])
			}
			return false, err
		}
		return false, nil
	}
	if idx >= len(jobs) {
		return false, fmt.Errorf("pre-deploy cursor index %d out of range (%d jobs declared; cursor is owned by the deployment driver)", idx, len(jobs))
	}
	job := jobs[idx]
	outcome, why := e.preDeployOutcome(ctx, taskID)
	switch outcome {
	case preDeployCompleted:
		if idx+1 < len(jobs) {
			// 串行推进：铸下一个。配额同上转有界等待。
			if _, err := e.mintPreDeployJob(ctx, d, spec, jobs, idx+1); err != nil {
				if errors.Is(err, ErrTaskQuota) {
					e.log.Error("pre-deploy: mint blocked by task quota, retrying until deadline", "deployment", d.ID, "err", err)
					return e.preDeployBoundedWait(ctx, d, jobs[idx+1])
				}
				return false, err
			}
			return false, nil
		}
		// 全部完成：游标落 done 并清等待截止（carrier 子相位首拍自设 L1）。
		fresh, err := e.transitAndReload(ctx, d,
			[]deployment.State{deployment.StateReleasing}, deployment.StateReleasing,
			func(m *deployment.Deployment) {
				m.PreDeploy = deployment.PreDeployDone
				m.ObserveDeadline = ""
			})
		if err != nil {
			return false, err
		}
		// 回写驱动行（jobs→carrier 交界，本函数唯一继续被调用方消费的返回
		// 点）：release 持有的 d 必须看见清空的 ObserveDeadline——否则 job
		// 完成观测晚于等待截止（控制面停机跨窗/环卡滞）时，旧截止被误当 L1
		// 截止判超时，健康载体假回滚（L1 窗从未开启却判超时）。
		*d = *fresh
		return true, nil
	case preDeployPending:
		deadline := parseDeadline(d.ObserveDeadline)
		if deadline == nil {
			// 铸造同事务落截止，此形态是行损坏——诚实失败优于自愈循环。
			return false, fmt.Errorf("pre-deploy job %q (task %s) lost its wait deadline", job.GetName(), taskID)
		}
		if e.clock.Now().After(*deadline) {
			// 超窗强停锚定 Task：超窗迁移 job 继续写库会与回放竞态。
			if _, err := e.StopTask(ctx, taskID, true); err != nil && !errors.Is(err, state.ErrNotFound) && !errors.Is(err, ErrTaskTerminal) {
				e.log.Error("pre-deploy: stop past-deadline task", "task", taskID, "err", err)
			}
			return false, fmt.Errorf("pre-deploy job %q (task %s) did not reach a terminal state within its wait window (deadline %s)", job.GetName(), taskID, d.ObserveDeadline)
		}
		return false, nil // 等待（tick 再进）
	default:
		return false, fmt.Errorf("pre-deploy job %q (task %s) %s", job.GetName(), taskID, why)
	}
}

// preDeployBoundedWait 是配额受阻的有界等待：落等待截止（本作业全预算）
// 后等下一拍重试；截止已过即失败。返回 (false, nil) = 继续等。
func (e *Engine) preDeployBoundedWait(ctx context.Context, d *deployment.Deployment, job *specv1.JobSpec) (bool, error) {
	deadline := parseDeadline(d.ObserveDeadline)
	if deadline != nil && e.clock.Now().After(*deadline) {
		return false, fmt.Errorf("pre-deploy job %q could not be minted within its wait window (project task quota; deadline %s)", job.GetName(), d.ObserveDeadline)
	}
	if deadline == nil {
		wait := state.FormatTime(e.clock.Now().Add(job.GetTtl().AsDuration() + e.opts.Deploy.PreDeployWaitGrace))
		if _, err := e.transitAndReload(ctx, d,
			[]deployment.State{deployment.StateReleasing}, deployment.StateReleasing,
			func(m *deployment.Deployment) { m.ObserveDeadline = wait }); err != nil {
			return false, err
		}
	}
	return false, nil
}

// preDeployOutcome 判定锚定 Task 的执行结局。
type preDeployVerdict int

const (
	preDeployPending   preDeployVerdict = iota // 未终态：继续等
	preDeployCompleted                         // Run 终态 (stopped, completed)
	preDeployFailed                            // 一切非完成结局（why 携带描述）
)

func (e *Engine) preDeployOutcome(ctx context.Context, taskID string) (preDeployVerdict, string) {
	t, err := e.repos.Tasks.Get(ctx, taskID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return preDeployFailed, "task row vanished"
		}
		return preDeployFailed, fmt.Sprintf("lookup failed: %v", err)
	}
	if t.State == task.StateDeleted {
		return preDeployFailed, "task was deleted"
	}
	if !t.State.Terminal() {
		return preDeployPending, ""
	}
	// Task 终态镜像以 Run 为真源：成败判定走判读单源（oneshotRunVerdict
	// 五档判别 + 域映射：job 等待面把 ttl/早停也判失败）。
	verdict, why, err := e.oneshotRunVerdict(ctx, taskID)
	if err != nil {
		return preDeployFailed, fmt.Sprintf("list terminal runs: %v", err)
	}
	switch verdict {
	case oneshotSucceeded:
		return preDeployCompleted, ""
	case oneshotPending:
		return preDeployFailed, "reached a terminal task state with no terminal run"
	default:
		return preDeployFailed, why
	}
}

// mintPreDeployJob 铸造第 idx 个 job 的 one-shot Task 并同事务记账/发事件/
// 推进游标（铸造骨架经 mintOneShotTask 原语；本函数保留 preDeploy 域差异
// ——JobSpec 校验/镜像解析/挂网装配 + 游标 transit 与 pre_deploy_job 事实
// 事件。spawnScheduleTask 同款防御：游标非空 ⇒ Task 行必在）。
func (e *Engine) mintPreDeployJob(ctx context.Context, d *deployment.Deployment, spec *specv1.AppSpec, jobs []*specv1.JobSpec, idx int) (*task.Task, error) {
	job := jobs[idx]
	if verr := specir.ValidateJob(fmt.Sprintf("app.pre_deploy_jobs[%d]", idx), job); verr != nil {
		return nil, fmt.Errorf("pre-deploy job %q: %w", job.GetName(), verr)
	}
	p := job.GetProcess()
	image, err := e.preDeployJobImage(ctx, d, job)
	if err != nil {
		return nil, err
	}
	networks, err := e.preDeployJobNetworks(ctx, d, spec, job)
	if err != nil {
		return nil, err
	}
	minted := &specv1.TaskSpec{
		SchemaVersion: specir.SchemaVersion,
		TtlSeconds:    int64(job.GetTtl().AsDuration().Seconds()),
		Process: &specv1.ProcessSpec{
			Name:     job.GetName(),
			Command:  p.GetCommand(),
			Env:      p.GetEnv(),
			Networks: networks,
		},
	}
	if p.GetResources() != nil {
		minted.Process.Resources = p.GetResources()
	}
	minted.Process.ImageOrigin = &specv1.ProcessSpec_Image{Image: image}
	if len(p.GetSecretRefs()) > 0 {
		minted.Process.SecretRefs = p.GetSecretRefs()
	}
	// 托管库引用随铸面透传（任务域 Ensure 前注入 FLEETLY_DB_* env）。
	minted.Process.DatabaseRefs = p.GetDatabaseRefs()
	// Config 挂载声明随铸面透传（任务域材料解析 Config 内容 → 目标路径挂载）。
	minted.Process.ConfigMounts = p.GetConfigMounts()
	wait := state.FormatTime(e.clock.Now().Add(job.GetTtl().AsDuration() + e.opts.Deploy.PreDeployWaitGrace))
	// 部署轴 Team 先行解析（P0-2）：steps 回调不携 ctx/tx，事务外解析
	//（App 轴 Team 不可变，无 TOCTOU 面）。
	depTeam := e.eventTeamOfApp(ctx, e.repos, d.AppID)
	return e.mintOneShotTask(ctx, spec.GetApp().GetProject(),
		func() (*specv1.TaskSpec, error) { return minted, nil },
		func(taskID string) []writeStep {
			cursor := fmt.Sprintf("%d:%s", idx, taskID)
			return []writeStep{{
				// 游标推进 + 等待截止（原地迁移：无状态事件；审计随 transit
				// 落）+ pre_deploy_job 事实事件（部署→Task 因果链）。
				write: func(ctx context.Context, s transact.Stores) error {
					return e.transit(ctx, s, d,
						[]deployment.State{deployment.StateReleasing}, deployment.StateReleasing,
						func(m *deployment.Deployment) { m.PreDeploy, m.ObserveDeadline = cursor, wait })
				},
				facts: []func() writes.FactRow{func() writes.FactRow {
					// 双真偏离（仅流面）：pre_deploy_job 事实事件的取证孪生由
					// 同步的 transit 四件一审计承载（deployment.transit 单行；
					// A 化前亦无独立审计行，行集合零漂移）。
					return writes.FactRow{
						Name: eventPreDeployJobFired, Aggregate: "deployment", ID: d.ID,
						Payload: preDeployJobEventPayloadJSON(d, idx, job.GetName(), taskID), TeamID: depTeam,
						StreamOnly: true,
					}
				}},
			}}
		},
		false)
}

// preDeployJobImage 解析 job 镜像：image 直用；from_build 经 buildDigests
// （Revision 级单产物，引用 by 进程名——job 引用的必须是声明 from_build 的
// App 进程）。digest 缺席 = 精确失败（releasing 进入即构建已完成，此处
// 失败只能是声明错配）。
func (e *Engine) preDeployJobImage(ctx context.Context, d *deployment.Deployment, job *specv1.JobSpec) (string, error) {
	if img := job.GetProcess().GetImage(); img != "" {
		return img, nil
	}
	from := job.GetProcess().GetFromBuild()
	digests, err := e.buildDigests(ctx, d.ToRevision)
	if err != nil {
		return "", fmt.Errorf("pre-deploy job %q: resolve build digests: %w", job.GetName(), err)
	}
	ref, ok := digests[from]
	if !ok {
		return "", fmt.Errorf("pre-deploy job %q: from_build %q has no build digest (it must name a process that declares from_build)", job.GetName(), from)
	}
	return ref, nil
}

// preDeployJobNetworks 解析 job 挂靠网（ADR-0030 决策 7）：缺省 = 项目全部
// 活跃网络（projectNetworkNames 单源——job 要连 db-<id>，App 连得上什么
// job 就连得上什么）；声明时三形态铸时解析为平台网络名（taskGroup: 翻译 /
// project: 跨 Project 严格——部署链恒 strict，未批准 fail-closed）。
func (e *Engine) preDeployJobNetworks(ctx context.Context, d *deployment.Deployment, spec *specv1.AppSpec, job *specv1.JobSpec) ([]string, error) {
	declared := job.GetProcess().GetNetworks()
	if len(declared) == 0 {
		return e.projectNetworkFactsNamesOnly(ctx, spec.GetApp().GetProject()), nil
	}
	peers, err := e.resolvePeerRefs(ctx, e.repos, spec.GetApp().GetProject(), spec, false)
	if err != nil {
		return nil, fmt.Errorf("pre-deploy job %q: %w", job.GetName(), err)
	}
	out := make([]string, 0, len(declared))
	for _, net := range declared {
		switch {
		case specir.IsNetworkGroupRef(net):
			out = append(out, TaskGroupNetworkName(specir.NetworkGroupName(net)))
		case specir.IsCrossProjectRef(net):
			ref, ok := peers.Refs[net]
			if !ok {
				// resolvePeerRefs strict 已在上方整体失败，此处不可达防御。
				return nil, fmt.Errorf("pre-deploy job %q: cross-project network %q is not approved", job.GetName(), net)
			}
			out = append(out, ref.Name)
		default:
			out = append(out, net)
		}
	}
	return out, nil
}

// CheckPreDeployNetworks 是受理面的 preDeployJobs 裸网名在场性预检（B12
// P3-5，fail-closed）：裸网名（非 taskGroup:/project: 引用形态——
// specir.IsNetworkGroupRef/IsCrossProjectRef 是形态判定真源）按项目内
// 平台网名解析，项目内无该网即拒绝入队——否则 typo 冻结进 spec，要到
// job Ensure（编排器面）超时才暴露。taskGroup:/project: 形态不在此重复
// 执法：前者缺组由受管域收敛创建，后者由 CheckPeerRefs strict 预检。
// 存储错误如实上抛（与受理面整体拒绝语义一致）。Revision 读取经调用方
// 事务 Runner（所见即受理终局；CheckPeerRefs 同款形态，各持一次读——
// 受理是冷路径，换取两个预检各自自洽）。
func (e *Engine) CheckPreDeployNetworks(ctx context.Context, s transact.Stores, projectID, revisionID string) error {
	rev, err := s.Revisions.Get(ctx, revisionID)
	if err != nil {
		return err
	}
	spec, err := unmarshalSpec(rev.Spec)
	if err != nil {
		return err
	}
	for i, j := range spec.GetPreDeployJobs() {
		for _, net := range j.GetProcess().GetNetworks() {
			if specir.IsNetworkGroupRef(net) || specir.IsCrossProjectRef(net) {
				continue
			}
			_, err := s.Networks.GetByName(ctx, projectID, net)
			if err == nil {
				continue
			}
			if errors.Is(err, state.ErrNotFound) {
				return fmt.Errorf("%w: pre-deploy job %q (app.pre_deploy_jobs[%d]) attaches network %q which does not exist in project %s; create the network, reference it as taskGroup:<group> or project:<project-id>/<name>, or drop the attachment",
					ErrPreDeployNetworkUnknown, j.GetName(), i, net, projectID)
			}
			return fmt.Errorf("resolve network %s in project %s: %w", net, projectID, err)
		}
	}
	return nil
}

// abandonPreDeployJobs 是取消/抢占的 job 收口（ADR-0030 决策 6）：游标
// 锚定的活跃 Task best-effort 强停（审计携带操作者；已终态不动；错误容忍
// 记日志——job 自带 ttl，收口失败也有界）。
func (e *Engine) abandonPreDeployJobs(ctx context.Context, d *deployment.Deployment) {
	_, taskID, anchored := d.PreDeployAnchor()
	if !anchored {
		return
	}
	t, err := e.repos.Tasks.Get(ctx, taskID)
	if err != nil {
		if !errors.Is(err, state.ErrNotFound) {
			e.log.Error("pre-deploy: abandon lookup", "deployment", d.ID, "task", taskID, "err", err)
		}
		return
	}
	if t.State.Terminal() {
		return
	}
	if _, err := e.StopTask(ctx, taskID, true); err != nil && !errors.Is(err, ErrTaskTerminal) {
		e.log.Error("pre-deploy: abandon stop", "deployment", d.ID, "task", taskID, "err", err)
	}
}

// PreDeployTaskID 返回部署当前锚定的 job Task（API/CLI 暴露面：无 job 或
// 已完成返回空）。
func PreDeployTaskID(d *deployment.Deployment) string {
	_, taskID, anchored := d.PreDeployAnchor()
	if !anchored {
		return ""
	}
	return taskID
}
