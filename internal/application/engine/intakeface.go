// intakeface.go 是 IntakeBuildFace 窄端口的核侧适配（P2-3）：把完整
// contract.Intake 接口（e.intake 持有）窄化为 Function 构建输入登记单方法
// ——functions 模块的消费面最小化（模块间禁 import 的窄化消费，PinVolumes
// 先例；enginebackref 判据：核持接口、适配面在核）。
package engine

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
)

// intakeBuildFace 适配 contract.Intake → contract.IntakeBuildFace。
type intakeBuildFace struct{ in contract.Intake }

func (f intakeBuildFace) PrepareFunctionBuild(ctx context.Context, b build.Build, fnRow function.Function, rev function.Revision) error {
	return f.in.PrepareFunctionBuild(ctx, &b, &fnRow, &rev)
}
