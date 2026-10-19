package engine

import (
	"context"
	"errors"
	"fmt"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// ErrProcessUnknown 是冻结 Spec 里不存在的进程名（含从未部署形态——无
// 冻结体即无进程可重启）。k3s 的 pod 驱逐对零命中静默，引擎侧先行校验
// 是防假成功的唯一防线（deliverygrpc 面 map 为精确错误码；engine 不
// import api 包——依赖方向，见架构 §2）。
var ErrProcessUnknown = errors.New("process: unknown process name")

// RestartProcess 强制重排单个进程的载体（ADR-0069，排障动线"进程卡死 →
// 就地重启"）：不动镜像不动 Spec、零 lineage（不产生 Revision/Deployment，
// 代戳不动），经 Runtime 可选重启子面强制重排该进程的全部载体。与
// ADR-0064 内容寻址 Ensure 互补——重发未变更 Spec 零替换，本动词是显式
// 强制面。进程存在性先对当前冻结 Spec 校验，再落运行时。
//
// 载体定位（R02-M3）：blue-green 进程的载体 ID 是代次化形态（ADR-0048
// 决策 1.4），裸 workloadID 在 swarm 未命中、在 k3s 零命中即假成功。载体
// 身份按**最新 succeeded Deployment 的 Generation** 解析（基线行是唯一
// 真源——不依赖进程内缓存，重启后同样可解析），两种形态（裸/代次化）按
// 基线 spec 的进程形态排首选序，未命中时才回退；provider 侧按 ID 精确命中
// 一个代次，命中即停（不做双重重排）。
func (e *Engine) RestartProcess(ctx context.Context, appID, process string) error {
	if appID == "" || process == "" {
		return fmt.Errorf("%w: app and process must not be empty", ErrProcessUnknown)
	}
	team, a, err := e.appTeam(ctx, appID)
	if err != nil {
		return err
	}
	rev, err := e.repos.Revisions.Latest(ctx, appID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return fmt.Errorf("%w: app %s has no frozen spec (never deployed)", ErrProcessUnknown, appID)
		}
		return err
	}
	frozen, err := unmarshalSpec(rev.Spec)
	if err != nil {
		return err
	}
	found := false
	for _, p := range frozen.GetProcesses() {
		if p.GetName() == process {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: process %q not in the frozen spec of app %s", ErrProcessUnknown, process, appID)
	}
	restart := capability.FacesOf(e.runtime).Restart
	if restart == nil {
		return ErrRuntimeNoRestart
	}
	ns := capability.NamespaceRef{Team: team, Project: a.ProjectID, App: appID}
	return e.restartCarrier(ctx, restart, ns, appID, process)
}

// restartCarrier 逐候选载体 ID 调 Provider 重启：未命中（运行时的
// capability.ErrRestartTargetNotFound 哨兵）才尝试下一形态；命中一次即
// 返回；其它错误如实上抛。全部候选未命中返回包了 state.ErrNotFound 的
// 错误（API 面 map E_NOT_FOUND，k3s 的零命中不再假成功）。
func (e *Engine) restartCarrier(ctx context.Context, restart capability.RuntimeRestart,
	ns capability.NamespaceRef, appID, process string) error {
	var lastErr error
	for _, carrierID := range e.restartCarrierCandidates(ctx, appID, process) {
		err := restart.Restart(ctx, ns, carrierID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, capability.ErrRestartTargetNotFound) {
			return err
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = capability.ErrRestartTargetNotFound
	}
	// 错误语义与既有 E_NOT_FOUND 一致：外层包 state.ErrNotFound（deliverygrpc
	// 的 mapStateError 据 errors.Is 映射），cause 链保留 provider 原错误。
	return fmt.Errorf("%w: carrier for process %q is not present in the runtime: %w", state.ErrNotFound, process, lastErr)
}

// restartCarrierCandidates 返回载体 ID 候选（按命中概率排序）。载体形态由
// 最新 succeeded 基线的 spec 决定：BG 进程的先代次化形态；rolling / 无基线
// / 基线 spec 不可解析时先裸形态（保持既有语义）。两种形态都列出是对「进程
// 形态在最近一次 Revision 变更过（未部署）」的防御——载体身份以物化时的
// 基线 spec 为准，不拿未部署的 Revision 猜。
func (e *Engine) restartCarrierCandidates(ctx context.Context, appID, process string) []string {
	base := WorkloadID(appID, process)
	d, err := e.repos.Deployments.LatestSucceeded(ctx, appID)
	if err != nil || d == nil {
		return []string{base}
	}
	scoped := genScopedWorkloadID(base, d.Generation)
	spec, err := e.loadSpec(ctx, d.ToRevision)
	if err != nil {
		return []string{base, scoped}
	}
	for _, p := range spec.GetProcesses() {
		if p.GetName() != process {
			continue
		}
		if p.GetStrategy() == specv1.DeployStrategy_DEPLOY_STRATEGY_BLUE_GREEN {
			return []string{scoped, base}
		}
		return []string{base, scoped}
	}
	return []string{base, scoped}
}
