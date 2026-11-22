// functionbuild.go 是 Function 构建源的应用模块消费面（P2-3，设计 v2 §3
// 机制三件②③）：PrepareFunctionBuild 是「inputs 登记」的 intake 导出入口
// （ADR-0102 收编条款——functions 经 contract.Intake 端口调用，模块间禁
// import）；铸行归受理面（deployPending），本面只负责在途行的幂等登记
// （D-4 重拾：daemon 重启后 queued/building 行由推进步重拾输入登记——
// App 侧 DriveBuilding 同纪律）与拾取前置检的 function 轴归属判定。
package intake

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
)

// PrepareFunctionBuild 保证 Function 构建行有进程内输入登记（D-4 根修的
// Function 轴；prepareBuildInput 原子性同源——检出目录已存在即跳过解包）。
// 登记键 = Build 行 ID（受理面铸造时同拍登记；重启后由环推进步经本面补）。
func (in *Intake) PrepareFunctionBuild(ctx context.Context, b *build.Build, fnRow *function.Function, rev *function.Revision) error {
	input, err := in.functionBuildInput(ctx, fnRow, rev)
	if err != nil {
		return err
	}
	in.build.inputsMu.Lock()
	in.build.inputs[b.ID] = input
	in.build.inputsMu.Unlock()
	return nil
}

// functionBuildInput 组装 Function 构建输入（v1 upload 单源；git 位留精确
// 失败——P2-4 同期件）。推送目标 = LocalImageRef（tag 形态：受管仓库地址 +
// Project 前纲 + fn repo；revSeq 是命名锚——与 App 侧 r<seq> tag 同构；
// Revision 冻结的是 digest，tag 只是推送与缓存命名，ADR-0019 附录 B.4）。
func (in *Intake) functionBuildInput(ctx context.Context, fnRow *function.Function, rev *function.Revision) (capability.BuildRequest, error) {
	spec := &specv1.FunctionSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(rev.Spec, spec); err != nil {
		return capability.BuildRequest{}, fmt.Errorf("unmarshal function spec: %w", err)
	}
	endpoint, err := in.registryEndpoint(ctx)
	if err != nil {
		return capability.BuildRequest{}, err
	}
	var contextDir string
	switch origin := spec.GetSource().GetKind().(type) {
	case *specv1.Source_Upload:
		dir, err := in.extractUploadContext(ctx, origin.Upload.GetId(), rev.ID)
		if err != nil {
			return capability.BuildRequest{}, err
		}
		contextDir = dir
	case *specv1.Source_Git:
		return capability.BuildRequest{}, fmt.Errorf("function git source is not supported yet (planned for the phase 2 cohort batch)")
	default:
		return capability.BuildRequest{}, fmt.Errorf("function build requires an upload source (v1)")
	}
	pushCred := &endpoint.Cred
	if projectCred, err := in.projectPushCred(ctx, fnRow.ProjectID, endpoint); err != nil {
		return capability.BuildRequest{}, err
	} else if projectCred != nil {
		pushCred = projectCred
	}
	if pushCred.Username == "" && pushCred.Secret == "" {
		pushCred = nil // 匿名仓库合法形态（受管 zot 恒有凭证；缺省留 nil）
	}
	input := capability.BuildRequest{
		Builder:    builderNameFunction(spec.GetBuild()),
		ContextDir: contextDir,
		Target:     LocalImageRef(endpoint.Addr, fnRow.ProjectID, fnRow.ID, rev.Seq),
		PushCred:   pushCred,
	}
	if strategy, ok := spec.GetBuild().GetStrategy().(*specv1.BuildSpec_Railpack); ok {
		input.Railpack = &capability.RailpackInput{PinnedVersion: strategy.Railpack.GetPinnedVersion()}
	}
	return input, nil
}

// builderNameFunction 按 FunctionSpec build 面路由 builder（受理位只放
// railpack；缺面 = dockerfile 防御轨，App 侧 builderName 同判据）。
func builderNameFunction(b *specv1.BuildSpec) string {
	if _, ok := b.GetStrategy().(*specv1.BuildSpec_Railpack); ok {
		return specir.BuilderRailpack
	}
	return specir.BuilderDockerfile
}

// functionBuildOwnerActive 报告 Function 构建行的归属是否活跃（拾取前置检
// 的 function 轴分支）：函数行 pending 锚指向该 revision = 归属活跃（环
// 推进步会重建登记）；pending 锚已被他写（latest-wins）或清位（已兑现）
// = 孤儿 → 一跳 cancelled。
func (in *Intake) functionBuildOwnerActive(ctx context.Context, b *build.Build) (bool, error) {
	row, err := in.repos.Functions.Get(ctx, b.FunctionID)
	if err != nil {
		return false, err
	}
	return row.PendingRevisionID == b.RevisionID, nil
}
