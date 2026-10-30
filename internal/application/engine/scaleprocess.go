package engine

// ScaleProcess 是进程副本伸缩的引擎半边（B1s，设计稿 §1.2 第二轮定稿）：
// 读当前生效 Spec（LatestSucceeded 的 to_revision 冻结体）→ 改目标进程
// replicas → SubmitSpec 提交（hook 在位走 MergeOverlay——冻结材料 +
// 提交件运行时；无 hook 直投 MergeNone）。ADR-0064 内容锚定等价保证
// scale-only 零载体替换；钳制/校验沿 ProcessSpec 既有 ValidateProcess
// 执法（负数在此拒绝——descriptor CEL 已拦一层，此处叶子复核纵深）。
// 豁免确认（Restart/Scale 豁免族）：Confirmation 恒 nil；admission 去重
// 语义与 Deploy 同面（幂等键/commit 空，latest-wins 合并照常）。

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// ScaleProcess 伸缩目标进程并提交（返回受理部署行与判定附注——响应面
// 复用 Admission outcome，与 DeployResponse 同构）。
func (e *Engine) ScaleProcess(ctx context.Context, appID, process string, replicas int64) (*deployment.Deployment, *Admission, error) {
	if appID == "" || process == "" {
		return nil, nil, fmt.Errorf("%w: app and process must not be empty", ErrProcessUnknown)
	}
	// 当前生效 Spec：LatestSucceeded → to_revision 冻结体（与
	// resolveRenderRevision 的 seq=0 同轴——「最新冻结≠生效」分野锚，
	// ADR-0071 决策 2；从未成功部署 = 无可伸缩对象）。
	d, err := e.repos.Deployments.LatestSucceeded(ctx, appID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, nil, fmt.Errorf("%w: app %s has no successful deployment to scale", ErrNoSuccessfulBaseline, appID)
		}
		return nil, nil, err
	}
	rev, err := e.repos.Revisions.Get(ctx, d.ToRevision)
	if err != nil {
		return nil, nil, err
	}
	effective, err := unmarshalSpec(rev.Spec)
	if err != nil {
		return nil, nil, err
	}
	// 目标进程改 replicas（克隆体上改——冻结体行不可写，改件经 SubmitSpec
	// 重新冻结）。未知进程精确拒绝（RestartProcess 同哨兵）。
	scaled := proto.Clone(effective).(*specv1.AppSpec)
	var target *specv1.ProcessSpec
	for _, p := range scaled.GetProcesses() {
		if p.GetName() == process {
			target = p
			break
		}
	}
	if target == nil {
		return nil, nil, fmt.Errorf("%w: process %q not in the effective spec of app %s", ErrProcessUnknown, process, appID)
	}
	target.Replicas = replicas
	if verr := spec.ValidateProcess(fmt.Sprintf("processes[%q]", process), target); verr != nil {
		return nil, nil, verr
	}
	// hook 在位 = MergeOverlay（冻结材料 + 提交件运行时——scale 只动运行时
	// 半边的 replicas）；无 hook 直投。SourceText 透传生效行的冻结原文
	//（compose 形态的原文保真）。
	hookOwned := false
	merge := MergeNone
	if _, herr := e.repos.Hooks.Get(ctx, appID); herr == nil {
		hookOwned = true
		merge = MergeOverlay
	} else if !errors.Is(herr, state.ErrNotFound) {
		return nil, nil, herr
	}
	_, out, adm, serr := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: appID, Spec: scaled, SourceText: rev.SourceText,
		HookOwned: hookOwned, Merge: merge,
		Submit: &SubmitOptions{},
	})
	if serr != nil {
		return nil, nil, serr
	}
	return out, adm, nil
}
