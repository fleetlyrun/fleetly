package swarm

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	mobycontainer "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// 载体命名与标记（Provider 私有，平台永不解析；架构 §5）。
const (
	// labelManaged 标记 fleetly 管辖的载体（列表/收敛的选择器）。
	labelManaged = "fleetly.managed"
	// 归属标记：ns 三元组 + Workload/Process。
	labelTeam     = "fleetly.ns.team"
	labelProject  = "fleetly.ns.project"
	labelApp      = "fleetly.ns.app"
	labelWorkload = "fleetly.workload.id"
	labelProcess  = "fleetly.process"
	// labelGeneration 搬运平台 Generation（幂等重放与 Drift 判定锚）。
	labelGeneration = "fleetly.generation"
	// labelPorts 记录声明端口（"8080/http,5432/tcp"；Addresses 回读用）。
	labelPorts = "fleetly.ports"

	// 节点锚定标记（D-MN-8：平台节点 ID 先于 placement 存在、永不复用）。
	labelNodeID = "fleetly.node.id"

	// namePrefix 是载体命名公式前缀：fleetly-<team>-<prj>-<app>-<proc>。
	namePrefix = "fleetly"
	// swarmServiceNameLimit 是 swarm 服务名上限（DNS label 约束 63）。
	swarmServiceNameLimit = 63
)

// serviceName 计算载体服务名：fleetly-<team>-<prj>-<app>-<proc>；超长时
// 截断并以稳定哈希后缀兜底（唯一性以 fleetly.* 标记锚定，架构 §5）。
func serviceName(ns capability.NamespaceRef, process string) string {
	full := strings.Join([]string{namePrefix, ns.Team, ns.Project, ns.App, process}, "-")
	full = sanitizeNamePart(full)
	if len(full) <= swarmServiceNameLimit {
		return full
	}
	// 截断保留前缀 + 短哈希：同输入稳定，不同输入低碰撞（碰撞兜底 = 平台
	// ID 标记，非名字）。
	sum := sha256.Sum256([]byte(full))
	suffix := hex.EncodeToString(sum[:])[:8]
	return full[:swarmServiceNameLimit-9] + "-" + suffix
}

// sanitizeNamePart 把名字片段压到 DNS label 安全集（小写字母数字与连字符）。
func sanitizeNamePart(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return namePrefix
	}
	return out
}

// portsLabelValue 序列化端口声明（Addresses 回读）。
func portsLabelValue(ports []capability.WorkloadPort) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, fmt.Sprintf("%d/%s", p.Port, p.Protocol))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// parsePortsLabel 还原端口声明。
func parsePortsLabel(v string) []capability.WorkloadPort {
	var ports []capability.WorkloadPort
	for _, part := range strings.Split(v, ",") {
		if part == "" {
			continue
		}
		slash := strings.LastIndex(part, "/")
		if slash < 0 {
			continue
		}
		n, err := strconv.Atoi(part[:slash])
		if err != nil || n < 1 || n > 65535 {
			continue
		}
		ports = append(ports, capability.WorkloadPort{
			// G109：n 已钳 [1,65535]，int32 无溢出面。
			Port:     int32(n), //nolint:gosec
			Protocol: capability.Protocol(part[slash+1:]),
		})
	}
	return ports
}

// workloadLabels 构造归属标记集。
func workloadLabels(ns capability.NamespaceRef, w capability.Workload, gen capability.Generation) map[string]string {
	return map[string]string{
		labelManaged:    "true",
		labelTeam:       sanitizeNamePart(ns.Team),
		labelProject:    sanitizeNamePart(ns.Project),
		labelApp:        sanitizeNamePart(ns.App),
		labelWorkload:   w.ID,
		labelProcess:    sanitizeNamePart(w.Process),
		labelGeneration: strconv.FormatUint(uint64(gen), 10),
		labelPorts:      portsLabelValue(w.Ports),
	}
}

// nsSelector 是隔离域的列表过滤器（label 全等匹配）。
func nsSelector(ns capability.NamespaceRef) map[string]string {
	return map[string]string{
		labelManaged: "true",
		labelTeam:    sanitizeNamePart(ns.Team),
		labelProject: sanitizeNamePart(ns.Project),
		labelApp:     sanitizeNamePart(ns.App),
	}
}

// toServiceSpec 把平台 Workload 翻译为 swarm ServiceSpec。
//
// 关键映射决策（真机坑对照）：
//   - 不发布宿主端口：Route 流量经 Edge（traefik）进 overlay 网络；
//     swarm 不应用 Hosts 且 nft 可能杀 DNAT，端口发布不可依赖。
//   - Env 排序：swarm update 以 spec 变更为准，排序保幂等 diff 稳定。
//   - Placement → 节点 label 约束公式（node.labels.fleetly.node.id==<id>）。
//   - Networks 按名引用（网络存在性由 engine 侧网络 reconciler 保证；
//     taskGroup:<name> 前缀由 engine 已翻译为实际网络名）。
func toServiceSpec(ns capability.NamespaceRef, w capability.Workload, gen capability.Generation, secretCarriers map[string]string) swarm.ServiceSpec {
	container := &swarm.ContainerSpec{
		Image:    w.Image,
		Labels:   workloadLabels(ns, w, gen),
		Command:  w.Command,
		Env:      envSlice(w.Env),
		Hostname: "{{.Service.Name}}",
	}
	if w.Healthcheck != nil {
		container.Healthcheck = toSwarmHealthcheck(w.Healthcheck)
	}
	for _, v := range w.Volumes {
		container.Mounts = append(container.Mounts, mount.Mount{
			Type:   mount.TypeVolume,
			Source: volumeCarrierName(v.VolumeID),
			Target: v.Target,
		})
	}
	// Secret 文件注入（值已落 swarm secret 载体；容器内 /run/secrets/<名>）。
	for platformName, carrier := range secretCarriers {
		container.Secrets = append(container.Secrets, &swarm.SecretReference{
			SecretName: carrier,
			File: &swarm.SecretReferenceFileTarget{
				Name: platformName,
				Mode: 0o400,
			},
		})
	}

	task := swarm.TaskSpec{
		ContainerSpec: container,
		RestartPolicy: &swarm.RestartPolicy{
			Condition: swarm.RestartPolicyConditionAny,
		},
	}
	if w.Resources != nil {
		task.Resources = &swarm.ResourceRequirements{
			Limits: &swarm.Limit{
				NanoCPUs:    cpuMillisToNano(w.Resources.CPUMillis),
				MemoryBytes: int64(w.Resources.MemoryMB) * 1024 * 1024,
			},
		}
	}
	if constraints := placementConstraints(w.Placement); len(constraints) > 0 {
		task.Placement = &swarm.Placement{Constraints: constraints}
	}
	for _, net := range w.Networks {
		task.Networks = append(task.Networks, swarm.NetworkAttachmentConfig{
			Target: carrierNetworkName(ns, net),
		})
	}

	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Name:   serviceName(ns, w.Process),
			Labels: container.Labels,
		},
		TaskTemplate: task,
		Mode:         replicasMode(w.Replicas),
		// 端口发布仅限受管 Edge 的部署形态（Workload.Publish 显式声明）；
		// 用户 Workload 一律不发布宿主端口（流量经 Edge，见函数注释）。
		EndpointSpec: endpointSpec(w.Publish),
		// UpdateConfig 语义由平台 Deployment 状态机掌管（滚动与回滚 =
		// Replay），编排器原生回滚不用（ADR-0005）。
		UpdateConfig: &swarm.UpdateConfig{
			Parallelism:   1,
			Order:         swarm.UpdateOrderStartFirst,
			FailureAction: swarm.UpdateFailureActionPause,
		},
	}
}

// endpointSpec 翻译宿主端口发布声明（routing mesh 模式；仅受管 Edge 形态使用）。
func endpointSpec(publish []capability.PortPublish) *swarm.EndpointSpec {
	if len(publish) == 0 {
		return nil
	}
	ports := make([]swarm.PortConfig, 0, len(publish))
	for _, p := range publish {
		ports = append(ports, swarm.PortConfig{
			Protocol:      network.TCP,
			PublishMode:   swarm.PortConfigPublishModeIngress,
			PublishedPort: uint32(p.PublishedPort), //nolint:gosec // 端口域 int32→uint32 无符号扩展
			TargetPort:    uint32(p.TargetPort),    //nolint:gosec
		})
	}
	return &swarm.EndpointSpec{Ports: ports}
}

// replicasMode 把期望副本数映射为服务模式（负数钳 0——排空态；钳后
// int64 → uint64 无溢出面）。
func replicasMode(replicas int64) swarm.ServiceMode {
	if replicas < 0 {
		replicas = 0
	}
	r := uint64(replicas) //nolint:gosec // 上方已钳非负
	return swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &r}}
}

// envSlice 把 env map 翻译为排序的 KEY=VALUE 切片（幂等 diff 稳定）。
func envSlice(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// toSwarmHealthcheck 把声明式探针翻译为 swarm 探针方言。
func toSwarmHealthcheck(h *capability.Healthcheck) *mobycontainer.HealthConfig {
	hc := &mobycontainer.HealthConfig{
		Interval:    h.Interval,
		Timeout:     h.Timeout,
		StartPeriod: h.StartPeriod,
		Retries:     int(h.Retries),
	}
	switch {
	case h.HTTPPath != "":
		hc.Test = []string{"CMD-SHELL", fmt.Sprintf(`wget -qO- http://127.0.0.1:%d%s || exit 1`, firstHTTPOrTCPPort(h), h.HTTPPath)}
	case h.TCPPort != 0:
		hc.Test = []string{"CMD-SHELL", fmt.Sprintf(`nc -z 127.0.0.1 %d || exit 1`, h.TCPPort)}
	case h.Exec != nil:
		hc.Test = h.Exec
	default:
		hc.Test = []string{"NONE"}
	}
	return hc
}

// firstHTTPOrTCPPort 为 http 探针取进程端口（探针未带端口时回落声明端口）。
func firstHTTPOrTCPPort(h *capability.Healthcheck) int32 {
	if h.TCPPort != 0 {
		return h.TCPPort
	}
	return 8080
}

// placementConstraints 把节点选择翻译为 swarm 约束公式（label 公式，
// 跨 Runtime 节点 ID 永不复用）。
func placementConstraints(p capability.Placement) []string {
	if len(p.NodeIDs) == 0 {
		return nil
	}
	constraints := make([]string, 0, len(p.NodeIDs))
	for _, id := range p.NodeIDs {
		constraints = append(constraints, "node.labels."+labelNodeID+"=="+id)
	}
	return constraints
}

// volumeCarrierName 是 Volume 的 swarm 载体名（Provider 私有）。
func volumeCarrierName(volumeID string) string {
	return "fleetly-vol-" + sanitizeNamePart(volumeID)
}

// cpuMillisToNano 毫核 → swarm NanoCPUs。
func cpuMillisToNano(millis int64) int64 {
	if millis <= 0 {
		return 0
	}
	return millis * 1_000_000
}

// encodeRegistryAuth 把拉取凭证编码为 X-Registry-Auth 形态（base64 JSON；
// 凭证不落载体 label 或明文 env，ADR-0014）。
func encodeRegistryAuth(c capability.RegistryCredential) (string, error) {
	payload, err := json.Marshal(map[string]string{
		"username":      c.Username,
		"password":      c.Secret,
		"serveraddress": c.Server,
	})
	if err != nil {
		return "", fmt.Errorf("encode registry auth: %w", err)
	}
	return base64.StdEncoding.EncodeToString(payload), nil
}
