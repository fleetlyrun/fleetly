// projection.go 是 Function 投影前段（设计 v3 §7）：冻结 FunctionSpec +
// 行事实（replicas）+ 项目事实 → 受管 Workload 单源公式。与三既有投影同族
// （ProjectDatabase 最近亲——受管单载体）；唯一消费者是本模块收敛步。
package functions

import (
	"fmt"
	"time"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

const (
	// fnProbeInterval/timeout/startPeriod/retries 是 handler 探针节拍：
	// 缺省 tcp 探声明口（不假设函数配合 /healthz——调用语义归函数自己，
	// 设计 §7）。
	fnProbeInterval    = 10 * time.Second
	fnProbeTimeout     = 5 * time.Second
	fnProbeStartPeriod = 15 * time.Second
	fnProbeRetries     = 3
	// fnStopGrace 是在途请求排空窗（设计 §7：缺省 30s）。
	fnStopGrace = 30 * time.Second
)

// ProjectFunction 把冻结 FunctionSpec + 行事实投影为受管 Workload。
// revisionDigest 是在服冻结体指纹（FLEETLY_FUNCTION_REVISION env 的前 12
// 形态来源）。行面旋钮（Replicas）由行直折——容量变更即投影变更即指纹
// 变更（scale-only 中间支，ADR-0064）；exposure 不进投影（接入平面与
// 载体无关，三平面分立）。
func ProjectFunction(s *specv1.FunctionSpec, row *function.Function, team string, networks []string, egress map[string]bool, revisionDigest string) (capability.Workload, capability.NamespaceRef, error) {
	if s.GetFunction().GetId() == "" || s.GetFunction().GetProject() == "" {
		return capability.Workload{}, capability.NamespaceRef{}, fmt.Errorf("function spec has no identity ref")
	}
	img := s.GetSource().GetImage()
	if img == nil || img.GetRef() == "" {
		return capability.Workload{}, capability.NamespaceRef{}, fmt.Errorf("function spec has no image source (v1 image-only)")
	}
	h := s.GetHandler()
	if h == nil || h.GetName() == "" {
		return capability.Workload{}, capability.NamespaceRef{}, fmt.Errorf("function spec has no handler process")
	}
	if len(h.GetPorts()) != 1 {
		return capability.Workload{}, capability.NamespaceRef{}, fmt.Errorf("function handler must declare exactly one port")
	}
	port := h.GetPorts()[0]
	if port.GetProtocol() == specv1.Protocol_PROTOCOL_TCP {
		return capability.Workload{}, capability.NamespaceRef{}, fmt.Errorf("function handler port protocol must be http or h2c")
	}
	// UNSPECIFIED 归一 http（受理位缺省归一的纵深）；枚举词面映射与核
	// protocolFromSpec 同判据（本域禁 tcp）。
	cp := capability.ProtocolHTTP
	if port.GetProtocol() == specv1.Protocol_PROTOCOL_H2C {
		cp = capability.ProtocolH2C
	}

	ns := capability.NamespaceRef{Team: team, Project: row.ProjectID,
		Function: row.ID, FunctionName: row.Name}

	// 平台事实 env 注入（only-add，FLEETLY_DB_* 同族先例，设计 §7）。
	env := make(map[string]string, len(h.GetEnv())+4)
	for k, v := range h.GetEnv() {
		env[k] = v
	}
	rev12 := revisionDigest
	if len(rev12) > 12 {
		rev12 = rev12[:12]
	}
	env["FLEETLY_FUNCTION_ID"] = row.ID
	env["FLEETLY_FUNCTION_NAME"] = row.Name
	env["FLEETLY_FUNCTION_REVISION"] = rev12
	env["FLEETLY_FUNCTION_PORT"] = fmt.Sprintf("%d", port.GetPort())

	w := capability.Workload{
		// Workload ID = 函数行 id（跨 Revision 稳定——Ensure 走 update 而
		// 非重建，Database 先例）。
		ID:       row.ID,
		Process:  h.GetName(),
		Image:    img.GetRef(),
		Command:  h.GetCommand(),
		Env:      env,
		Replicas: row.Replicas,
		// 常驻语义（v1 min≥1；RestartAlways 由 Provider 缺省承载，显式
		// 声明防歧义）。
		Restart:        capability.RestartAlways,
		Networks:       networks,
		EgressNetworks: contract.EgressSubset(networks, egress),
		StopGrace:      fnStopGrace,
		Ports: []capability.WorkloadPort{
			{Port: port.GetPort(), Protocol: cp},
		},
		Resources: resources(h.GetResources()),
		Addressing: []capability.Address{
			// fn-<id> 与 exposure 无关的永久属性（三律①）：private 可达域
			// 与 public 合成路由解析同一声明。
			{Name: contract.FunctionDNSName(row.ID)},
		},
	}
	// 探针：声明优先（http_path/tcp_port/exec 三态由 spec 携带——capability
	// 探针是全解析 IR，HTTPPath 需 HTTPPort 回填）；缺省 tcp 探声明口。
	w.Healthcheck = healthcheck(h.GetHealthcheck(), port.GetPort())
	return w, ns, nil
}

// resources 折 spec 资源面（nil 面 = 无限制）。
func resources(r *specv1.ResourcesSpec) *capability.Resources {
	if r == nil || (r.GetCpuMillis() == 0 && r.GetMemoryMb() == 0) {
		return nil
	}
	return &capability.Resources{CPUMillis: r.GetCpuMillis(), MemoryMB: r.GetMemoryMb()}
}

// healthcheck 把 spec 探针折为 capability IR：缺省 tcp 探声明口。
func healthcheck(hc *specv1.HealthcheckSpec, declaredPort int32) *capability.Healthcheck {
	if hc == nil {
		return &capability.Healthcheck{
			TCPPort:     declaredPort,
			Interval:    fnProbeInterval,
			Timeout:     fnProbeTimeout,
			StartPeriod: fnProbeStartPeriod,
			Retries:     fnProbeRetries,
		}
	}
	out := &capability.Healthcheck{
		TCPPort:     declaredPort,
		Interval:    fnProbeInterval,
		Timeout:     fnProbeTimeout,
		StartPeriod: fnProbeStartPeriod,
		Retries:     fnProbeRetries,
	}
	switch p := hc.GetProbe().(type) {
	case *specv1.HealthcheckSpec_HttpPath:
		out.HTTPPath = p.HttpPath
		out.HTTPPort = declaredPort
	case *specv1.HealthcheckSpec_TcpPort:
		out.TCPPort = p.TcpPort
	case *specv1.HealthcheckSpec_Exec:
		out.Exec = p.Exec.GetCommand()
	}
	if hc.GetInterval() != nil {
		out.Interval = hc.GetInterval().AsDuration()
	}
	if hc.GetTimeout() != nil {
		out.Timeout = hc.GetTimeout().AsDuration()
	}
	if hc.GetStartPeriod() != nil {
		out.StartPeriod = hc.GetStartPeriod().AsDuration()
	}
	if hc.GetRetries() > 0 {
		out.Retries = hc.GetRetries()
	}
	return out
}
