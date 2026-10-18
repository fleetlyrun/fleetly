package engine

// ADR-0083 步骤 6：声明式重启的执行时 seq-only 判定。判定延迟到驱动器
// 拾取时（prepare 态早期）对**实际最新 succeeded 基线**重算 To/From 树
// diff——受理时判定的 From 锚可能已被更早排队的部署推进，受理时判定会让
// 「回退中间变更却免跑迁移」的组合静默通过（对抗审查 B8）。

import (
	"context"
	"errors"
	"fmt"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// markRestartSeqOnly 在 prepare 态早期执行 seq-only 判定（ADR-0083）：
// kind 非 rollback 且存在 succeeded 基线且基线→目标的树 diff ⊆ restart_seq
// 时，就地补标 kind=restart（审计 AfterFP 后缀）+ PreDeploy 游标直落
// done（迁移不因重启重跑）。返回刷新行；nil = 未命中（照常推进）。
//
// 判定护栏（逐条对应裁决条款）：
//   - d.PreDeploy 已是 done：回滚部署（受理时直落 done，Rollback 永不
//     重跑 jobs）或已补标过的行（崩溃重驱幂等）——跳过；
//   - 无 succeeded 基线（首次部署）或基线 == 目标（同内容重放）：非重启
//     语义——同 Revision 再部署重跑 preDeployJobs 的既有纪律保持；
//   - diff 非空且全部落在 restart_seq：命中。BG 策略 spec 一并命中
//     （kind=restart 不改物化舞步，release 按 anyBlueGreen 分流照常）。
func (e *Engine) markRestartSeqOnly(ctx context.Context, d *deployment.Deployment, toSpec *specv1.AppSpec) (*deployment.Deployment, error) {
	if d.PreDeploy == deployment.PreDeployDone {
		return nil, nil // 回滚行 / 已补标行：判定不重入
	}
	base, err := e.repos.Deployments.LatestSucceeded(ctx, d.AppID)
	if errors.Is(err, state.ErrNotFound) {
		return nil, nil // 首次部署：无基线可 diff，正常推进
	}
	if err != nil {
		return nil, fmt.Errorf("resolve baseline: %w", err)
	}
	if base.ToRevision == d.ToRevision {
		return nil, nil // 同内容重放：不是重启（preDeployJobs 重跑纪律保持）
	}
	fromSpec, err := e.loadSpec(ctx, base.ToRevision)
	if err != nil {
		return nil, fmt.Errorf("load baseline spec %s: %w", base.ToRevision, err)
	}
	seqOnly, err := specir.RestartSeqOnly(fromSpec, toSpec)
	if err != nil {
		return nil, err
	}
	if !seqOnly {
		return nil, nil
	}
	return e.markRestart(ctx, d)
}

// markRestart 就地补标 kind=restart（ADR-0083 步骤 6/7）：经 transition.go
// 的注记前门原地迁移（LedgerOnly 审计单行，kind 随 AfterFP "; kind=restart"
// 后缀带出——回滚受理形态的执行时镜像）。崩溃在补标与下一态之间 = 重驱时
// 判定护栏按 done 短路，幂等。
func (e *Engine) markRestart(ctx context.Context, d *deployment.Deployment) (*deployment.Deployment, error) {
	return e.transitDeploymentAnnotated(ctx, d,
		func(m *deployment.Deployment) { m.PreDeploy = deployment.PreDeployDone },
		"; kind="+KindRestart)
}
