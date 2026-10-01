package engine

import (
	"fmt"
	"strings"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	specir "github.com/fleetlyrun/fleetly/internal/spec"
)

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
			// 且跨 Deployment 稳定，Ensure 走 update 路径而非反复重建）。
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
		for _, port := range p.GetPorts() {
			w.Ports = append(w.Ports, capability.WorkloadPort{
				Port:     port.GetPort(),
				Protocol: protocolFromSpec(port.GetProtocol()),
			})
		}
		if hc := p.GetHealthcheck(); hc != nil {
			w.Healthcheck = &capability.Healthcheck{
				HTTPPath:    hc.GetHttpPath(),
				TCPPort:     hc.GetTcpPort(),
				Exec:        hc.GetExec().GetCommand(),
				Interval:    hc.GetInterval().AsDuration(),
				Timeout:     hc.GetTimeout().AsDuration(),
				StartPeriod: hc.GetStartPeriod().AsDuration(),
				Retries:     hc.GetRetries(),
			}
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
	w.Addressing = []capability.Address{
		{Name: TaskDNSName(t.GetTask().GetId())},
		{Name: RunDNSName(runID)},
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
