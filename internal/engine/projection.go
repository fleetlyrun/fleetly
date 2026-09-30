package engine

import (
	"fmt"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Project 把 Revision 冻结的 AppSpec 投影为 Runtime 无关的 Workload 集
// （架构 §4 投影规则：探针/卷钉住/网络附件在 IR 是声明，映射成编排器
// 原语是 Provider 的事）。Team 取 Project 行（归属轴），调用方负责解析。
//
// from_build 解析：buildDigests 提供同名构建产物 digest（Ensure 前由平台
// 解析——Revision 只冻结引用，产物随 Build 行走）；缺失即校验错误。
func Project(spec *specv1.AppSpec, team string, buildDigests map[string]string) ([]capability.Workload, capability.NamespaceRef, error) {
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
			})
		}
		workloads = append(workloads, w)
	}
	return workloads, ns, nil
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
