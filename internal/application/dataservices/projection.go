package dataservices

// Database 投影前段（ADR-0029）：DatabaseSpec（IR 单真源）→ 用户域受管
// Workload 的单源公式。P5 wave-4 随域拆包自 engine/projection.go 迁入
//（唯一消费者；探测节拍常量与 egress 过滤随迁——egress 公式真源在
// contract.EgressSubset，与核投影面 Project/ProjectTask 同一单源）。

import (
	"fmt"
	"time"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// dbProbeConnectTimeout 是 DB Workload 探针节拍（启动宽限给足首次初始化；
// 模板只出探针 argv，节拍属投影侧）。
const dbProbeConnectTimeout = 10 * time.Second

// ProjectDatabase 把 DatabaseSpec 投影为用户域受管 Workload（ADR-0029）：
// Database 域 ns（NamespaceRef.Database 轴）；镜像/端口/卷目标来自模板
// 注册表（模板参数不可变，无 Revision 冻结面——IR 仍是投影单真源）。
// dbVolumeID 是挂靠卷行 id（= 库名确定性公式解析出的行；ADR-0097：卷附件
// 携带行 id，载体键 fleetly-vol-<行 id>，运行时零 name）；dbName 是数据库
// 名（载体名 name 段来源 fleetly-db-<name>-<id>，ADR-0092 不动）。挂网
// networks 是 Project 活跃网络全集（受理位已保证非空）。staticSettings 是
// 喂入律（ADR-0095 决策 4 / design §3.2/§4.3）的投影面输入：调用方经
// databasetemplate.SplitSettings 预过滤的 static 子集——dynamic 键永不进 Workload
// env/command（守卫 TestDynamicKeysAbsentFromProjection 执法）。
func ProjectDatabase(s *specv1.DatabaseSpec, team, dbVolumeID, dbName string, networks []string, egress map[string]bool, tpl databasetemplate.Template, limits *capability.Resources, staticSettings map[string]string) (capability.Workload, capability.NamespaceRef, error) {
	if s.GetDatabase().GetId() == "" || s.GetDatabase().GetProject() == "" {
		return capability.Workload{}, capability.NamespaceRef{}, fmt.Errorf("database spec has no identity ref")
	}
	ns := capability.NamespaceRef{Team: team, Project: s.GetDatabase().GetProject(),
		Database: s.GetDatabase().GetId(), DatabaseName: dbName}
	env, command := tpl.Workload(staticSettings)
	w := capability.Workload{
		// Workload ID = Database 行 ID（域内唯一、跨收敛稳定——Ensure 走
		// update 路径而非反复重建）。
		ID:       s.GetDatabase().GetId(),
		Process:  tpl.Engine(),
		Image:    tpl.Image(),
		Command:  command,
		Env:      env,
		Replicas: 1,
		Networks: networks,
		// egress 事实（数据库挂全部活跃项目网；egress:none 网络的成员
		// 语义对数据面无损——deny 只挡载体主动出站，服务入站与域内互通
		// 由放行集承载，ADR-0052 决策 6）。
		EgressNetworks: contract.EgressSubset(networks, egress),
		// 数据面停止宽限：pg/redis 干净关停（含恢复期）远超编排器缺省的
		// 10s——滚动替换窗口硬杀会把 WAL/AOF 留在损坏态（staging pgvector
		// 事故实证，2026-10-03）。60s 给足快速关停与检查点收口。
		StopGrace: 60 * time.Second,
		Ports: []capability.WorkloadPort{
			{Port: tpl.Meta().Port, Protocol: capability.ProtocolTCP},
		},
		Healthcheck: &capability.Healthcheck{
			// 引擎原生 exec 探针（模板单源：pg_isready/redis-cli——镜像必带；
			// 通用 TCP 方言的 nc 假设在 bookworm 系镜像不成立，真机实证）。
			Exec:        tpl.Probe(),
			Interval:    dbProbeConnectTimeout,
			Timeout:     5 * time.Second,
			StartPeriod: 60 * time.Second, // 首启初始化（initdb/AOF）给足宽限
			Retries:     3,
		},
		Volumes: []capability.VolumeMount{
			// VolumeID = 卷行 id（ADR-0097 决策 1：载体键 fleetly-vol-<行
			// id>——行 id 全局唯一，跨项目同名库不再共键）。
			{VolumeID: dbVolumeID, Target: tpl.DataTarget()},
		},
		Addressing: []capability.Address{{Name: contract.DatabaseDNSName(s.GetDatabase().GetId())}},
		// 载体资源面（ADR-0095 决策 7：行值 static 语义——nil = 无限制 =
		// 现状；limits 与 staticSettings 同为行值直折——spec IR 的
		// settings/resources 扩列挂账后续批，行单真源语义不受影响）。
		Resources: limits,
	}
	return w, ns, nil
}
