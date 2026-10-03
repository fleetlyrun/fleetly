package engine

import (
	"fmt"
	"strings"
	"time"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine/dbtemplate"
	specir "github.com/fleetlyrun/fleetly/internal/spec"
)

// resolvedHealthcheck 把 spec 冻结探针解析为全解析 IR（探针策略单源在
// engine——Provider 只做原语映射，架构评审第二轮候选 7）：
// ① http 探针端口回退链：探针自带 tcp_port > 进程声明首端口 > 8080
// （无任何端口声明时的诚实缺省；与此前 swarm Provider 内的同链逐值一致，
// 策略回归引擎）；② exec 方言归一：proto 面可能直写的 docker 方言前缀
// （CMD/CMD-SHELL）在投影期展开为干净 argv（CMD-SHELL 载荷经 sh -c 承载
// ——spec 归一层 compose 面同款语义）。
func resolvedHealthcheck(hc *specv1.HealthcheckSpec, ports []capability.WorkloadPort) *capability.Healthcheck {
	out := &capability.Healthcheck{
		HTTPPath:    hc.GetHttpPath(),
		TCPPort:     hc.GetTcpPort(),
		Exec:        normalizeProbeExec(hc.GetExec().GetCommand()),
		Interval:    hc.GetInterval().AsDuration(),
		Timeout:     hc.GetTimeout().AsDuration(),
		StartPeriod: hc.GetStartPeriod().AsDuration(),
		Retries:     hc.GetRetries(),
	}
	if out.HTTPPath != "" {
		out.HTTPPort = probeHTTPPort(out.TCPPort, ports)
	}
	return out
}

// probeHTTPPort 解析 http 探针端口回退链：探针自带 tcp_port 优先（spec
// oneof 下不可与 http_path 共存，防直接构造 IR 的输入）> 进程声明首端口
// > 8080（无任何声明的诚实缺省）。与此前 swarm Provider 内的同链逐值
// 一致——策略回归引擎（架构评审第二轮候选 7）。
func probeHTTPPort(tcpPort int32, ports []capability.WorkloadPort) int32 {
	if tcpPort != 0 {
		return tcpPort
	}
	if len(ports) > 0 {
		return ports[0].Port
	}
	return 8080
}

// normalizeProbeExec 归一 exec 探针方言：CMD 前缀剥壳；CMD-SHELL 载荷
// （单个 shell 字符串）经 sh -c 承载——按空白切分会撕碎引号结构（staging
// 真机实证 2026-10-02）；无前缀已是干净 argv 直通。
func normalizeProbeExec(exec []string) []string {
	if len(exec) == 0 {
		return exec
	}
	switch exec[0] {
	case "CMD":
		return exec[1:]
	case "CMD-SHELL":
		return []string{"sh", "-c", strings.Join(exec[1:], " ")}
	default:
		return exec
	}
}

// PeerRefs 是跨 Project 引用的解析结果（ADR-0013 附录 A.3）：spec 引用串
// （project:<id>/<name>）→ 解析后的跨域引用。缺席语义由模式决定：
// strict（Isolate=false）缺席 = fail-closed 错误；isolate 缺席 = 剥离
// （撤销隔离收敛）。解析真源在 engine（peers.go resolvePeerRefs）。
type PeerRefs struct {
	Refs    map[string]capability.NetworkRef
	Isolate bool
}

// Project 把 Revision 冻结的 AppSpec 投影为 Runtime 无关的 Workload 集
// （架构 §4 投影规则：探针/卷钉住/网络附件在 IR 是声明，映射成编排器
// 原语是 Provider 的事）。Team 取 Project 行（归属轴），调用方负责解析。
//
// from_build 解析：buildDigests 提供同名构建产物 digest（Ensure 前由平台
// 解析——Revision 只冻结引用，产物随 Build 行走）；缺失即校验错误。
// 跨 Project 引用经 peers 翻译为 NetworkRefs（ADR-0013 附录 A.3）。
func Project(spec *specv1.AppSpec, team string, buildDigests map[string]string, peers PeerRefs) ([]capability.Workload, capability.NamespaceRef, error) {
	ns := capability.NamespaceRef{Team: team, Project: spec.GetApp().GetProject(), App: spec.GetApp().GetId()}
	workloads := make([]capability.Workload, 0, len(spec.GetProcesses()))
	for _, p := range spec.GetProcesses() {
		image, err := resolveImageOrigin(p, buildDigests)
		if err != nil {
			return nil, ns, err
		}
		w := capability.Workload{
			// Workload ID 是确定性合成串（领域模型 §3：ULID 或等价——唯一
			// 且跨 Deployment 稳定，Ensure 走 update 路径而非重建）。
			ID:      WorkloadID(spec.GetApp().GetId(), p.GetName()),
			Process: p.GetName(),
			Image:   image,
			Command: p.GetCommand(),
			Env:     p.GetEnv(),
			Replicas: func() int64 {
				if r := p.GetReplicas(); r > 0 {
					return r
				}
				return 1 // 缺省单副本
			}(),
			Networks: p.GetNetworks(),
		}
		// 网络别名 = 进程名（ADR-0034：compose 服务名互访语义补全——
		// Provider 把 Addressing 映射为挂靠网络的别名；Task 域双级 DNS
		// 同通道，ADR-0025 决策 6）。
		w.Addressing = []capability.Address{{Name: p.GetName()}}
		for _, port := range p.GetPorts() {
			w.Ports = append(w.Ports, capability.WorkloadPort{
				Port:     port.GetPort(),
				Protocol: protocolFromSpec(port.GetProtocol()),
			})
		}
		if hc := p.GetHealthcheck(); hc != nil {
			w.Healthcheck = resolvedHealthcheck(hc, w.Ports)
		}
		if res := p.GetResources(); res != nil {
			w.Resources = &capability.Resources{CPUMillis: res.GetCpuMillis(), MemoryMB: res.GetMemoryMb()}
		}
		if pl := p.GetPlacement(); pl != nil {
			w.Placement.NodeIDs = pl.GetNodeIds()
		}
		for _, vol := range p.GetVolumes() {
			w.Volumes = append(w.Volumes, capability.VolumeMount{
				VolumeID: vol.GetVolumeId(),
				Target:   vol.GetTarget(),
				ReadOnly: vol.GetReadOnly(),
			})
		}
		// taskGroup 翻译责任在 engine 投影层（ADR-0025 决策 5）：声明的
		// 网络组名（taskGroup:<name> 跨挂）在此翻译为实际平台网络名再
		// 下发——Provider 收到的 Networks 一律是平台网络名。跨 Project
		// 引用（project:<id>/<name>）经 peers 解析为 NetworkRefs（ADR-0013
		// 附录 A.3）：approved → 跨域附件；未 approved 由模式裁决
		//（strict 错误 / isolate 剥离）——同域 Networks 不混入跨域形态。
		networks := make([]string, 0, len(w.Networks))
		for _, net := range w.Networks {
			switch {
			case specir.IsNetworkGroupRef(net):
				networks = append(networks, TaskGroupNetworkName(specir.NetworkGroupName(net)))
			case specir.IsCrossProjectRef(net):
				ref, ok := peers.Refs[net]
				if !ok {
					if peers.Isolate {
						continue // 隔离收敛：未批准引用剥离（A.4）
					}
					return nil, ns, fmt.Errorf(
						"process %q references cross-project network %q which is not approved by the receiving project; declare and approve the peer, or remove the reference (ADR-0013)",
						p.GetName(), net)
				}
				w.NetworkRefs = append(w.NetworkRefs, ref)
			default:
				networks = append(networks, net)
			}
		}
		w.Networks = networks
		workloads = append(workloads, w)
	}
	return workloads, ns, nil
}

// ProjectTask 把 TaskSpec 的一个 Run 投影为 Runtime 无关 Workload（Task
// 域 ns：NamespaceRef.Task 轴，ADR-0025 决策 4）。Run Workload 一律
// RestartNever（退出即终态——resident 池的补足由平台承担，不靠编排器
// 重启）；Addressing 携带双级稳定 DNS（池级轮询 + per-Run 稳定名）；
// network_group 翻译为平台网络名（同决策 5）。
//
// process.networks 是 firstBootJobs 铸造面的挂靠通道（ADR-0030 决策 7）：
// API 受理面禁用该字段（validateTaskCommon），engine 铸造的部署期 job 例外
// ——铸造时已解析为平台网络名（taskGroup:/project: 引用不复存在），此处
// 原样透传。
func ProjectTask(t *specv1.TaskSpec, team string, runID string, running bool) (capability.Workload, capability.NamespaceRef, error) {
	if t.GetProcess().GetImage() == "" {
		return capability.Workload{}, capability.NamespaceRef{}, fmt.Errorf("task %s process has no image origin (build source is an app-only surface)", t.GetTask().GetId())
	}
	ns := capability.NamespaceRef{Team: team, Project: t.GetTask().GetProject(), Task: t.GetTask().GetId()}
	p := t.GetProcess()
	w := capability.Workload{
		ID:       runID,
		Process:  "run",
		Image:    p.GetImage(),
		Command:  p.GetCommand(),
		Env:      p.GetEnv(),
		Replicas: 1,
		Restart:  capability.RestartNever,
	}
	if !running {
		w.Replicas = 0 // 排空中：缩零承载 SIGTERM+StopGrace 停止路径
	}
	if res := p.GetResources(); res != nil {
		w.Resources = &capability.Resources{CPUMillis: res.GetCpuMillis(), MemoryMB: res.GetMemoryMb()}
	}
	if g := t.GetNetworkGroup(); g != "" {
		w.Networks = []string{TaskGroupNetworkName(g)}
	}
	// 部署期 job 的挂靠网（铸时解析、平台网络名原样；与 network_group
	// 并存时合并去重——铸造面二选一，防御性合并不产生第二语义）。
	if nets := p.GetNetworks(); len(nets) > 0 {
		merged := append([]string(nil), w.Networks...)
		for _, net := range nets {
			if !containsString(merged, net) {
				merged = append(merged, net)
			}
		}
		w.Networks = merged
	}
	w.Addressing = []capability.Address{
		{Name: TaskDNSName(t.GetTask().GetId())},
		{Name: RunDNSName(runID)},
	}
	return w, ns, nil
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// dbProbeConnectTimeout 是 DB Workload 探针节拍（启动宽限给足首次初始化；
// 模板只出探针 argv，节拍属投影侧）。
const dbProbeConnectTimeout = 10 * time.Second

// ProjectDatabase 把 DatabaseSpec 投影为用户域受管 Workload（ADR-0029）：
// Database 域 ns（NamespaceRef.Database 轴）；镜像/端口/卷目标来自模板
// 注册表（模板参数不可变，无 Revision 冻结面——IR 仍是投影单真源）。
// volumeName 是挂靠卷的 Project 内卷名（= 数据库名的确定性公式）；挂网
// networks 是 Project 活跃网络全集（受理位已保证非空）。
func ProjectDatabase(s *specv1.DatabaseSpec, team, volumeName string, networks []string, tpl dbtemplate.Template) (capability.Workload, capability.NamespaceRef, error) {
	if s.GetDatabase().GetId() == "" || s.GetDatabase().GetProject() == "" {
		return capability.Workload{}, capability.NamespaceRef{}, fmt.Errorf("database spec has no identity ref")
	}
	ns := capability.NamespaceRef{Team: team, Project: s.GetDatabase().GetProject(), Database: s.GetDatabase().GetId()}
	env, command := tpl.Workload()
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
			{VolumeID: volumeName, Target: tpl.DataTarget()},
		},
		Addressing: []capability.Address{{Name: DatabaseDNSName(s.GetDatabase().GetId())}},
	}
	return w, ns, nil
}

// TaskGroupNetworkName 把 Task Network Group 名翻译为平台网络名（Project
// 域；App Process 的 taskGroup:<name> 跨挂与 Task 自身 network_group 挂靠
// 在此归一——单一翻译真源，ADR-0025 决策 5）。
func TaskGroupNetworkName(group string) string {
	return "taskgrp-" + strings.ToLower(group)
}

// TaskDNSName 铸 per-Task 池级稳定 DNS 名（活 Run 轮询解析；公式住 engine
// ——名字是平台 API 面，N4 换 Runtime 名字不变，ADR-0025 决策 6/R-3）。
func TaskDNSName(taskID string) string {
	return "task-" + strings.ToLower(taskID)
}

// RunDNSName 铸 per-Run 稳定 DNS 名（同决策 6：engine 铸名公式真源）。
func RunDNSName(runID string) string {
	return "run-" + strings.ToLower(runID)
}

// WorkloadID 合成平台 Workload ID（app ULID + process 名：唯一、稳定、
// 人读可分解——归属与 Drift 判定的锚，Provider 把它搬运到载体标记）。
// 公式真源在此导出：API/CLI 面按 process 过滤日志等场景复用，不得各自
// 拼接（散落副本一旦漂移即静默失配）。
func WorkloadID(appID, process string) string {
	return appID + "-" + process
}

func resolveImageOrigin(p *specv1.ProcessSpec, buildDigests map[string]string) (string, error) {
	switch origin := p.GetImageOrigin().(type) {
	case *specv1.ProcessSpec_Image:
		return origin.Image, nil
	case *specv1.ProcessSpec_FromBuild:
		digest, ok := buildDigests[p.GetName()]
		if !ok {
			return "", fmt.Errorf("process %q references from_build with no successful build digest", p.GetName())
		}
		return digest, nil
	default:
		return "", fmt.Errorf("process %q has no image origin", p.GetName())
	}
}

func protocolFromSpec(proto specv1.Protocol) capability.Protocol {
	switch proto {
	case specv1.Protocol_PROTOCOL_H2C:
		return capability.ProtocolH2C
	case specv1.Protocol_PROTOCOL_TCP:
		return capability.ProtocolTCP
	default:
		return capability.ProtocolHTTP
	}
}
