// Spec 所有权分裂合并（ADR-0072）：材料半边（source、build）归部署来源
// 所有，运行时半边（processes 形状字段、pre_deploy_jobs）归声明方所有。
// 本文件只持纯函数——hook 存在性判定是 API 层（Deploy 受理位与 webhook
// 路径）的事。
package spec

import (
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"google.golang.org/protobuf/proto"
)

// MergeHookOwned 把材料半边（material 的 source/build）并入运行时半边
// （runtime 的其余全部字段），返回新对象（入参不改）。ADR-0072 决策 2/3
// 的唯一合并单源：webhook push（material = hook 合成的新材料，runtime =
// 当前冻结 Spec）与 hook 应用 Deploy(spec_file) overlay（material = 冻结
// Spec，runtime = 提交件——提交件的 source 由本合并天然丢弃）共用。
//
// 所有权字段表：source、build 取 material；schema_version、app、processes
// （含全部形状字段与显式 image 进程）、pre_deploy_jobs 取 runtime。边界
// 语义（ADR-0072 决策 6）：image→git 迁移场景 runtime 的显式 image 进程
// 被保留（adopt git 材料 = 进程改 from_build，是显式声明行为）；from_build
// 引用按 Revision 自身解析（产物 digest 在投影期组合，不受合并影响）。
func MergeHookOwned(material, runtime *specv1.AppSpec) *specv1.AppSpec {
	merged := proto.Clone(runtime).(*specv1.AppSpec)
	merged.Source = material.GetSource()
	merged.Build = material.GetBuild()
	return merged
}
