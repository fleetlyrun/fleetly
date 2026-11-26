package contract

import (
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Observations 是观测域句柄（批 1①b，设计稿 §3.5 ObservationSource 面）：
// 立域时经 Engine.BindDomain(teamOf) 铸造，句柄固定一个归属域——模块拿到
// 的句柄铸不出别域的归属（域判别随句柄收口，核内封闭枚举与全域铸造函数
// 退役）。载体事件的资源轴 Team 解析闭包随域在 BindDomain 登记，发射时由
// 核 ownerTeam 查表调用（发射时解析、失败 fail-closed 落哨兵）。本叶子只
// 依赖 domain 类型——应用模块包类型不得穿过本叶子。
type Observations interface {
	// RecordOwners 登记归属与 Generation（固定锚：域内 ID 显式传入——
	// database/pubsub/bucketstore/browse/function 域形态；就绪门集合界定）。
	RecordOwners(id string, gen uint64, ws []capability.Workload)
	// RecordOwnersByWorkload 逐载体登记（域内锚自载体派生——system 域以
	// 进程名为锚）。
	RecordOwnersByWorkload(gen uint64, ws []capability.Workload)
	// RecordProjected 写投影 spec 账本（不写 gen 锚的 detect 形态；合成路由
	// 期望集的消费面）。
	RecordProjected(id string, ws []capability.Workload)
	// RecordExpected 登记稳态看门狗期望锚（replicas>0 才登记——故意缩零
	// 不是事故）。
	RecordExpected(id string, gen uint64)
	// ForgetExpected 清稳态看门狗期望锚（域内收口动作）。
	ForgetExpected(id string)
}
