package swarm

import (
	"crypto/sha256"
	"encoding/hex"
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
	// 归属标记：ns 四元组（App/Task/Database 域主体互斥，ADR-0025 决策 4 /
	// ADR-0029）+ Workload/Process。
	labelTeam     = "fleetly.ns.team"
	labelProject  = "fleetly.ns.project"
	labelApp      = "fleetly.ns.app"
	labelTask     = "fleetly.ns.task"
	labelDatabase = "fleetly.ns.database"
	labelWorkload = "fleetly.workload.id"
	labelProcess  = "fleetly.process"
	// labelGeneration 搬运平台 Generation（幂等重放与 Drift 判定锚）。
	labelGeneration = "fleetly.generation"
	// labelPorts（声明端口 label）已退役（架构评审第二轮候选 7：Addresses
	// 期望集注入取代平行编码）；升级期存量 service 上的旧 label 由一次性
	// 滚动更新消除（spec diff 变化，staging 在役代价已付）。

	// 节点锚定标记（D-MN-8：平台节点 ID 先于 placement 存在、永不复用）。
	labelNodeID = "fleetly.node.id"

	// namePrefix 是载体命名公式前缀：fleetly-<team>-<prj>-<app>-<proc>。
	namePrefix = "fleetly"
	// runNamePrefix 是 Task 域 Run 载体命名公式前缀：fleetly-run-<run id>
	//（Run Workload ID = run id，域内天然唯一；ADR-0025 决策 7 混合拓扑的
	// per-Run service）。
	runNamePrefix = "fleetly-run"
	// dbNamePrefix 是 Database 域载体命名公式前缀：fleetly-db-<database id>
	//（Workload ID = database 行 ID，域内唯一；ADR-0029）。
	dbNamePrefix = "fleetly-db"
	// swarmServiceNameLimit 是 swarm 服务名上限（DNS label 约束 63）。
	swarmServiceNameLimit = 63
)

// workloadServiceName 计算载体服务名：App 域 = fleetly-<team>-<prj>-<app>-
// <proc>；Task 域 = fleetly-run-<run id>（决策 4/7）；Database 域 =
// fleetly-db-<database id>（ADR-0029）。超长时截断并以稳定哈希后缀兜底
// （唯一性以 fleetly.* 标记锚定，架构 §5）。
func workloadServiceName(ns capability.NamespaceRef, w capability.Workload) string {
	var full string
	switch {
	case ns.Task != "":
		full = strings.Join([]string{runNamePrefix, w.ID}, "-")
	case ns.Database != "":
		full = strings.Join([]string{dbNamePrefix, w.ID}, "-")
	default:
		full = strings.Join([]string{namePrefix, ns.Team, ns.Project, ns.App, w.Process}, "-")
	}
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

// workloadLabels 构造归属标记集（App/Task/Database 域主体互斥：Task 与
// Database 域不带空 labelApp——空值标记会破选择器全等匹配）。声明端口
// 不再入 label（ Addresses 期望集注入取代平行编码——架构评审第二轮
// 候选 7；升级期 label 消失触发存量 service 一次性滚动更新，staging 在役
// 代价已付）。
func workloadLabels(ns capability.NamespaceRef, w capability.Workload, gen capability.Generation) map[string]string {
	labels := map[string]string{
		labelManaged:    "true",
		labelTeam:       sanitizeNamePart(ns.Team),
		labelProject:    sanitizeNamePart(ns.Project),
		labelWorkload:   w.ID,
		labelProcess:    sanitizeNamePart(w.Process),
		labelGeneration: strconv.FormatUint(uint64(gen), 10),
	}
	switch {
	case ns.Task != "":
		labels[labelTask] = sanitizeNamePart(ns.Task)
	case ns.Database != "":
		labels[labelDatabase] = sanitizeNamePart(ns.Database)
	default:
		labels[labelApp] = sanitizeNamePart(ns.App)
	}
	return labels
}

// nsSelector 是隔离域的列表过滤器（label 全等匹配；域主体按 App/Task/
// Database 轴分支，与 workloadLabels 同构）。
func nsSelector(ns capability.NamespaceRef) map[string]string {
	selector := map[string]string{
		labelManaged: "true",
		labelTeam:    sanitizeNamePart(ns.Team),
		labelProject: sanitizeNamePart(ns.Project),
	}
	switch {
	case ns.Task != "":
		selector[labelTask] = sanitizeNamePart(ns.Task)
	case ns.Database != "":
		selector[labelDatabase] = sanitizeNamePart(ns.Database)
	default:
		selector[labelApp] = sanitizeNamePart(ns.App)
	}
	return selector
}

// toServiceSpec 把平台 Workload 翻译为 swarm ServiceSpec。
//
// 关键映射决策（真机坑对照）：
//   - 不发布宿主端口：Route 流量经 Edge（traefik）进 overlay 网络；
//     swarm 不应用 Hosts 且 nft 可能杀 DNAT，端口发布不可依赖。
//   - Env 排序：swarm update 以 spec 变更为准，排序保幂等 diff 稳定。
//   - Placement → 节点 label 约束公式（node.labels.fleetly.node.id==<id>）。
//   - Networks 按名引用（网络存在性由 engine 侧网络 reconciler 保证；
//     taskGroup:<name> 前缀已由 engine 投影层翻译为实际网络名，ADR-0025
//     决策 5）。
//   - Restart → swarm 重启策略（never=none：one-shot Run 退出即终态，
//     ADR-0025 决策 1）；StopGrace → StopGracePeriod。
//   - Addressing → 网络别名（平台标准 DNS 名的 swarm 原语映射，ADR-0025
//     决策 6；跨服务 alias 的 DNS RR 行为 e2e 实证后定稿）。
func toServiceSpec(ns capability.NamespaceRef, w capability.Workload, gen capability.Generation, secretCarriers map[string]secretCarrier) swarm.ServiceSpec {
	container := &swarm.ContainerSpec{
		Image:    w.Image,
		Labels:   workloadLabels(ns, w, gen),
		Command:  w.Command,
		Env:      envSlice(w.Env),
		Hostname: "{{.Service.Name}}",
	}
	if w.StopGrace > 0 {
		grace := w.StopGrace
		container.StopGracePeriod = &grace
	}
	if w.Healthcheck != nil {
		container.Healthcheck = toSwarmHealthcheck(w.Healthcheck)
	}
	for _, v := range w.Volumes {
		container.Mounts = append(container.Mounts, mount.Mount{
			Type:     mount.TypeVolume,
			Source:   volumeCarrierName(v.VolumeID),
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
		})
	}
	// 宿主只读绑定（受管采集面专用，ADR-0041：cadvisor 的 / /var/run /sys
	// /var/lib/docker）。bind 的 Source 是宿主绝对路径——路径缺席在任务
	// 启动期失败面呈现（Ensure 不预检）。
	for _, b := range w.HostBinds {
		container.Mounts = append(container.Mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   b.Source,
			Target:   b.Target,
			ReadOnly: b.ReadOnly,
		})
	}
	// Secret 文件注入（值已落 swarm secret 载体；容器内 /run/secrets/<名>）。
	// 按 platformName 排序后遍历（对照 envSlice 先例）：map 遍历序随机，
	// 排序保 spec 逐字节稳定——幂等重放的 diff 不产生假变更（P1-14）。
	// 引用 id+名双发（swarmkit validateSecretRefsSpec 要求）；UID/GID 显式
	// "0"——docker CLI 客户端补零而 raw API 空串会让 agent 的 strconv.Atoi
	// 在容器启动期炸掉（staging 真机实证 2026-10-02，同 id+名双发同批）。
	// Mode 0444（docker/compose 生态缺省）：0400 会把非 root USER 镜像
	//（torchwood/messageloop 皆 10001）挡在文件外——staging 真机实证
	// Permission denied → env 导出空串 → 启动期 fail-closed。
	// SkipMaterials：域材料默认挂全域 Workload；显式退出面（ADR-0041 的
	// cadvisor——无状态采集端不接收存储凭证，且其镜像无 /run/secrets 目录，
	// secret mountpoint 建在只读 overlay 上会启动失败，staging 实证）。
	if w.SkipMaterials {
		secretCarriers = nil
	}
	for _, platformName := range sortedKeys(secretCarriers) {
		c := secretCarriers[platformName]
		container.Secrets = append(container.Secrets, &swarm.SecretReference{
			SecretID:   c.id,
			SecretName: c.name,
			File: &swarm.SecretReferenceFileTarget{
				Name: platformName,
				UID:  "0",
				GID:  "0",
				Mode: 0o444,
			},
		})
	}

	task := swarm.TaskSpec{
		ContainerSpec: container,
		RestartPolicy: &swarm.RestartPolicy{
			// ADR-0025 决策 1：生命周期声明映射（缺省/always=any；
			// never=none——Run Workload 一律 never）。
			Condition: restartPolicyCondition(w.Restart),
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
	aliases := addressAliases(w.Addressing)
	for _, net := range w.Networks {
		task.Networks = append(task.Networks, swarm.NetworkAttachmentConfig{
			Target:  carrierNetworkName(ns, net),
			Aliases: aliases,
		})
	}
	// 跨域网络引用（受管 Edge 挂项目网）：载体名按引用自身的域解析——
	// 域名与载体名公式都是 Provider 私有，engine 只发引用形态（B1）。
	// 别名不挂跨域附件（平台 DNS 名是域内 API 面）。
	for _, ref := range w.NetworkRefs {
		task.Networks = append(task.Networks, swarm.NetworkAttachmentConfig{
			Target: carrierNetworkName(ref.Namespace, ref.Name),
		})
	}

	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Name:   workloadServiceName(ns, w),
			Labels: container.Labels,
		},
		TaskTemplate: task,
		Mode:         serviceMode(w),
		// 端口发布仅限受管形态的部署声明（Workload.Publish 显式——Edge
		// 80/443、受管 zot 5000）；用户 Workload 一律不发布宿主端口（流量
		// 经 Edge，见函数注释）。
		EndpointSpec: endpointSpec(w.Publish),
		// UpdateConfig 语义由平台 Deployment 状态机掌管（滚动与回滚 =
		// Replay），编排器原生回滚不用（ADR-0005）。顺序按争用面分流
		// （rolloutOrder）：挂卷负载必须 stop-first——start-first 的新任务
		// 与单副本卷钉住互斥，抢不到卷只会让 swarm 超时硬杀旧任务
		// （staging pgvector WAL 损坏事故实证，2026-10-03）；host 发布负载
		// 同理必须 stop-first（宿主端口节点级排他，N2 评审 P2-5）。
		UpdateConfig: &swarm.UpdateConfig{
			Parallelism:   1,
			Order:         rolloutOrder(w),
			FailureAction: swarm.UpdateFailureActionPause,
		},
	}
}

// rolloutOrder 把滚动顺序按争用面分流：挂卷负载 stop-first（数据卷
// 单写者，先停旧再起新；硬杀窗见 toServiceSpec UpdateConfig 注释），
// host 发布负载同样 stop-first（宿主端口直绑在节点上排他——start-first
// 滚动的新旧 task 同节点共存必争位：全局形态尤为必然，新 task 须落在
// 每个节点，与旧 task 抢同一宿主端口直到滚动卡死。staging cadvisor
// 首启滚动卡死实证 2026-10-04，runbook 记录·四/N2 评审 P2-5），其余
// 负载维持 start-first（先起新再停旧，无争用面、切换更平滑）。
func rolloutOrder(w capability.Workload) swarm.UpdateOrder {
	if len(w.Volumes) > 0 || hasHostPublish(w) {
		return swarm.UpdateOrderStopFirst
	}
	return swarm.UpdateOrderStartFirst
}

// hasHostPublish 报告 Workload 是否声明 host 模式端口发布
// （PublishModeHost）。判定刻意不看 Global/Replicas 形态：端口排他性
// 来自 host 直绑本身——全局形态保证滚动期新旧 task 同节点，replicated
// 单副本在单节点集群同样同节点。在翻译层统一执法（而非 provider 声明处
// 逐个标注）：第二个 host 端口服务自动同款，无防呆缺口。
func hasHostPublish(w capability.Workload) bool {
	for _, p := range w.Publish {
		if p.Mode == capability.PublishModeHost {
			return true
		}
	}
	return false
}

// restartPolicyCondition 把平台生命周期声明映射为 swarm 重启条件（ADR-0025
// 决策 1：长运行=any、one-shot=never）。
func restartPolicyCondition(r capability.RestartPolicy) swarm.RestartPolicyCondition {
	if r == capability.RestartNever {
		return swarm.RestartPolicyConditionNone
	}
	return swarm.RestartPolicyConditionAny
}

// addressAliases 把平台标准 DNS 名声明映射为 swarm 网络别名（排序稳定：
// 幂等 diff 逐字节稳定）。
func addressAliases(addressing []capability.Address) []string {
	if len(addressing) == 0 {
		return nil
	}
	names := make([]string, 0, len(addressing))
	for _, a := range addressing {
		names = append(names, sanitizeNamePart(a.Name))
	}
	sort.Strings(names)
	return names
}

// endpointSpec 翻译宿主端口发布声明（受管 Edge/zot/VL/VM 形态使用）。
// Mode 显式 vip——服务端对空 Mode 物化为 vip，发送形态与回读形态
// 一致是 no-op 比对的前提（staging 真机实证：受管 zot 恒不等 → update 风暴
// → 滚动替换把无钉住载体漂到无卷节点，2026-10-02）。PublishMode 缺省 =
// mesh（既有 Workload 零值兼容）；host 模式 = 宿主网络栈直绑（受管
// cadvisor 每节点端点，ADR-0041）。
func endpointSpec(publish []capability.PortPublish) *swarm.EndpointSpec {
	if len(publish) == 0 {
		return nil
	}
	ports := make([]swarm.PortConfig, 0, len(publish))
	for _, p := range publish {
		mode := swarm.PortConfigPublishModeIngress
		if p.Mode == capability.PublishModeHost {
			mode = swarm.PortConfigPublishModeHost
		}
		ports = append(ports, swarm.PortConfig{
			Protocol:      network.TCP,
			PublishMode:   mode,
			PublishedPort: uint32(p.PublishedPort), //nolint:gosec // 端口域 int32→uint32 无符号扩展
			TargetPort:    uint32(p.TargetPort),    //nolint:gosec
		})
	}
	return &swarm.EndpointSpec{Mode: swarm.ResolutionModeVIP, Ports: ports}
}

// serviceMode 把调度形态声明映射为 swarm 服务模式：Global 声明（受管采集
// 面每节点一 task，ADR-0041）优先；否则 Replicated（负数钳 0——排空态；
// 钳后 int64 → uint64 无溢出面）。
func serviceMode(w capability.Workload) swarm.ServiceMode {
	if w.Global {
		return swarm.ServiceMode{Global: &swarm.GlobalService{}}
	}
	replicas := w.Replicas
	if replicas < 0 {
		replicas = 0
	}
	r := uint64(replicas) //nolint:gosec // 上方已钳非负
	return swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &r}}
}

// sortedKeys 返回 map 键的排序切片：map 遍历序随机，翻译路径凡 map →
// 切片的落点都必须经此归一（幂等 diff 逐字节稳定；守卫 E 钉死）。
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// envSlice 把 env map 翻译为排序的 KEY=VALUE 切片（幂等 diff 稳定）。
func envSlice(env map[string]string) []string {
	keys := sortedKeys(env)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// toSwarmHealthcheck 把声明式探针翻译为 swarm 探针方言。探针是全解析
// IR（架构评审第二轮候选 7）：http 端口由 engine 解析为 h.HTTPPort（回退
// 链单源在 engine 投影），exec 恒干净 argv（方言归一在投影期）——本函数
// 只做原语映射。shell 探针钉死 busybox 兼容形态（N1 审查 P1-11）：`nc -z`
// 是 GNU/openbsd 扩展（busybox nc 无 -z，distroless 干脆无 nc——恒失败把
// 健康载体打成 unhealthy），`wget -qO-` 的合并短旗标在 busybox wget 上
// 不可靠。镜像假设在 Provider Describe Notes 声明。
func toSwarmHealthcheck(h *capability.Healthcheck) *mobycontainer.HealthConfig {
	hc := &mobycontainer.HealthConfig{
		Interval:    h.Interval,
		Timeout:     h.Timeout,
		StartPeriod: h.StartPeriod,
		Retries:     int(h.Retries),
	}
	switch {
	case h.HTTPPath != "":
		hc.Test = []string{"CMD-SHELL", fmt.Sprintf(`wget -q -O /dev/null http://127.0.0.1:%d%s || exit 1`, h.HTTPPort, h.HTTPPath)}
	case h.TCPPort != 0:
		// stdin 立即 EOF + -w 超时：连接建立即探活成功（busybox nc 无 -z
		// 的等价形态），拒绝/超时非零退出。
		hc.Test = []string{"CMD-SHELL", fmt.Sprintf(`nc -w 2 127.0.0.1 %d </dev/null || exit 1`, h.TCPPort)}
	case h.Exec != nil:
		// IR 的 exec 探针恒干净 argv；docker 探针 Test 方言要求首元素为
		// CMD/CMD-SHELL——裸 argv 会被 daemon 当作无探针（State.Health
		// 物化为 none，任务永滞 starting；staging 真机实证 2026-10-02，
		// F1.15 真机件⑥）。
		hc.Test = append([]string{"CMD"}, h.Exec...)
	default:
		hc.Test = []string{"NONE"}
	}
	return hc
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

// encodeRegistryAuth 已单源化至 capability.EncodeRegistryAuth（拉取凭证
// 的 X-Registry-Auth 形态三面共用；2026-10-03 架构评审候选 5 收口）。
