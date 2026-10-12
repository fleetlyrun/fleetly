package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state"
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
func (e *Engine) RestartProcess(ctx context.Context, appID, process string) error {
	if appID == "" || process == "" {
		return fmt.Errorf("%w: app and process must not be empty", ErrProcessUnknown)
	}
	team, a, err := e.appTeam(ctx, appID)
	if err != nil {
		return err
	}
	rev, err := e.revisions.Latest(ctx, e.db.Runner(), appID)
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
	restart, ok := e.runtime.(capability.RuntimeRestart)
	if !ok {
		return ErrRuntimeNoRestart
	}
	ns := capability.NamespaceRef{Team: team, Project: a.ProjectID, App: appID}
	return restart.Restart(ctx, ns, WorkloadID(appID, process))
}
