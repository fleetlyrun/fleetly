package k3s

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// 载体命名与标记（Provider 私有，平台永不解析；架构 §5）。label 公式与
// swarm Provider 同构（六轴域主体互斥 + workload/process/generation）。
const (
	labelManaged  = "fleetly.managed"
	labelTeam     = "fleetly.ns.team"
	labelProject  = "fleetly.ns.project"
	labelApp      = "fleetly.ns.app"
	labelTask     = "fleetly.ns.task"
	labelDatabase = "fleetly.ns.database"
	labelBrowse   = "fleetly.ns.browse"
	labelWorkload = "fleetly.workload.id"
	labelProcess  = "fleetly.process"
	// labelGeneration 搬运平台 Generation（幂等重放与 Drift 判定锚）。
	labelGeneration = "fleetly.generation"
	// labelEgress 标记挂 egress:none 网络的载体（ADR-0052 决策 6：per-carrier
	// deny 的 podSelector 锚——载体挂任一 egress:none 网络即整体限制出站）。
	labelEgress = "fleetly.egress"
	// netLabelPrefix 是网络成员资格 label 的 key 前缀（ADR-0054 决策 1：附件
	// 集翻译为成员资格 label，同域网与跨域引用按 (projectID, name) 复合同
	// 公式推导——接收方自己的网与挂靠方的引用得到同一 key，跨 ns 放行规则
	// 两侧天然对齐）。
	netLabelPrefix = "fleetly.net."
	// labelNetNone 是零附件载体的隔离锚（k8s 无 policy 选中即全通——零附件
	// 载体必须显式选中收口，与 swarm 零附件不可达逐位对齐）。
	labelNetNone = "fleetly.net.none"
	// labelNodeID 是节点锚定标记（D-MN-8：平台节点 ID 先于 placement 存在、
	// 永不复用；k8s Node 对象 label，Placement 的 nodeSelector 锚）。
	labelNodeID = "fleetly.node.id"

	namePrefix = "fleetly"
	// namespacePrefix 是 per-Project Namespace 命名公式前缀：
	// fleetly-<project>（ADR-0052 决策 3：Namespace 即互通域）。
	namespacePrefix = "fleetly"
	// systemNamespace 是受管域 Namespace（traefik/zot/VL/VM/cadvisor 载体；
	// swarm 受管域载体名前缀的对应物；与用户项目域分立）。
	systemNamespace = "fleetly-system"
	// runNamePrefix 是 Task 域 Run 载体命名公式前缀：fleetly-run-<run id>。
	runNamePrefix = "fleetly-run"
	// dbNamePrefix 是 Database 域载体命名公式前缀：fleetly-db-<database id>。
	dbNamePrefix = "fleetly-db"
	// browseNamePrefix 是 Browse 会话域载体命名公式前缀。
	browseNamePrefix = "fleetly-browse"
	// dnsLabelLimit 是 k8s 对象名上限（DNS label 约束 63）。
	dnsLabelLimit = 63
	// defaultVolumeStorage 是 PVC 名义请求量（Workload.VolumeMount 契约无
	// size 面；local-path provisioner 为 hostPath 型后端、不 enforce 请求量，
	// 名义值只影响调度器容量视图——ADR-0052 决策 7）。
	defaultVolumeStorage = "10Gi"
	// dnsPort/dnsNamespace 是 egress deny 放行集的 kube-system DNS 锚
	//（deny-all 立即断服务发现，k8s netpol 语义硬要求）。
	dnsNamespace = "kube-system"
	dnsPort      = 53
)

// workloadGeneration 解析载体生效 Generation（ADR-0048 双代窗）：逐载体
// 覆写优先，0 = 沿用 Ensure 调用 gen（rolling 存量零漂移）。与 swarm Provider
// 同源语义。
func workloadGeneration(w capability.Workload, gen capability.Generation) uint64 {
	if w.Generation != 0 {
		return w.Generation
	}
	return uint64(gen)
}

// namespaceName 计算 Namespace（per-Project，ADR-0052 决策 3）。受管域
// （无 Project 锚）落 systemNamespace。
func namespaceName(ns capability.NamespaceRef) string {
	if ns.Project == "" {
		return systemNamespace
	}
	return namespacePrefix + "-" + sanitizeNamePart(ns.Project)
}

// workloadName 计算载体对象名（Deployment/DaemonSet/Pod/Service 共用）：
// App 域 = fleetly-<app>-<proc>；Task 域 = fleetly-run-<run id>；Database
// 域 = fleetly-db-<database id>；Browse 域 = fleetly-browse-<session id>。
// 代次化载体（GenerationScoped，ADR-0048 决策 1.4）追加 -g<gen> 后缀——
// 双代窗两代各自独立载体。超长截断 + 稳定哈希兜底（唯一性以平台 ID 标记
// 锚定）。Namespace 已承载 project 段，App 域名不带 team/prj（与 swarm 名
// 公式分立——名字是 Provider 私有公式）。
func workloadName(ns capability.NamespaceRef, w capability.Workload, gen capability.Generation) string {
	var full string
	switch {
	case ns.Task != "":
		full = strings.Join([]string{runNamePrefix, w.ID}, "-")
	case ns.Database != "":
		full = strings.Join([]string{dbNamePrefix, w.ID}, "-")
	case ns.Browse != "":
		full = strings.Join([]string{browseNamePrefix, w.ID}, "-")
	case w.GenerationScoped:
		full = strings.Join([]string{namePrefix, ns.App, w.Process,
			"g" + strconv.FormatUint(workloadGeneration(w, gen), 10)}, "-")
	default:
		full = strings.Join([]string{namePrefix, ns.App, w.Process}, "-")
	}
	full = sanitizeNamePart(full)
	if len(full) <= dnsLabelLimit {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	return full[:dnsLabelLimit-9] + "-" + hex.EncodeToString(sum[:])[:8]
}

// pvcName 是 Volume 的 PVC 载体名（平台 Volume ID 跨 Runtime 保持，载体
// 名公式与 swarm volumeCarrierName 同构）。
func pvcName(volumeID string) string {
	return "fleetly-vol-" + sanitizeNamePart(volumeID)
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

// workloadLabels 构造归属标记集（域主体按 App/Task/Database/Browse 轴互斥，
// swarm workloadLabels 同构）。egress 载体标记、网络成员资格标记与
// addressing 标记在此合入。
func workloadLabels(ns capability.NamespaceRef, w capability.Workload, gen capability.Generation) map[string]string {
	labels := map[string]string{
		labelManaged:    "true",
		labelTeam:       sanitizeNamePart(ns.Team),
		labelProject:    sanitizeNamePart(ns.Project),
		labelWorkload:   w.ID,
		labelProcess:    sanitizeNamePart(w.Process),
		labelGeneration: strconv.FormatUint(workloadGeneration(w, gen), 10),
	}
	switch {
	case ns.Task != "":
		labels[labelTask] = sanitizeNamePart(ns.Task)
	case ns.Database != "":
		labels[labelDatabase] = sanitizeNamePart(ns.Database)
	case ns.Browse != "":
		labels[labelBrowse] = sanitizeNamePart(ns.Browse)
	default:
		labels[labelApp] = sanitizeNamePart(ns.App)
	}
	if len(w.EgressNetworks) > 0 {
		labels[labelEgress] = "true"
	}
	for k, v := range netMembershipLabels(ns, w) {
		labels[k] = v
	}
	for _, a := range w.Addressing {
		labels[addressingLabelKey(a.Name)] = "true"
	}
	return labels
}

// netMembershipLabels 构造 Workload 的网络成员资格标记（ADR-0054 决策 1）：
// 同域附件（Networks）与跨域引用（NetworkRefs）都落成员 label；零附件载体
// 落 none 锚；Publish 载体豁免（hostPort 发布 = 节点级可达语义，入站不隔离
// ——外部→hostPort 的 DNAT 流量源无 pod 身份，被 policy 选中的载体会对之
// 落默认拒，受管 Proxy/zot/cadvisor 的宿主发布面会整体断流）。
func netMembershipLabels(ns capability.NamespaceRef, w capability.Workload) map[string]string {
	if len(w.Publish) > 0 {
		return nil
	}
	if len(w.Networks) == 0 && len(w.NetworkRefs) == 0 {
		return map[string]string{labelNetNone: "true"}
	}
	out := make(map[string]string, len(w.Networks)+len(w.NetworkRefs))
	for _, n := range w.Networks {
		out[netLabelKey(ns.Project, n)] = "true"
	}
	for _, r := range w.NetworkRefs {
		out[netLabelKey(r.Namespace.Project, r.Name)] = "true"
	}
	return out
}

// netLabelKey 是成员资格 label 的完整 key（复合名段见 netMembershipName）。
func netLabelKey(projectID, networkName string) string {
	return netLabelPrefix + netMembershipName(projectID, networkName)
}

// netMembershipName 计算 (projectID, networkName) 复合的成员资格名段：名段
// 可读 + 项目哈希消歧（同项目内同名网与跨项目引用不碰撞；项目 ID 段不直
// 接入名——ULID 26 字符会把 key 顶爆）。超长截断 + 稳定哈希兜底，
// addressingLabelKey 同模式。
func netMembershipName(projectID, networkName string) string {
	name := sanitizeNamePart(networkName)
	if len(name) > 38 {
		sum := sha256.Sum256([]byte(name))
		name = name[:29] + "-" + hex.EncodeToString(sum[:])[:8]
	}
	sum := sha256.Sum256([]byte(projectID + "/" + networkName))
	return name + "-" + hex.EncodeToString(sum[:])[:6]
}

// addressingLabelKey 把平台 DNS 名声明映射为 pod label key（Service 的
// selector 锚——池级名选全部声明该名的载体（task-<id> RR 全部 run），per-Run
// 名只选自身；k8s label selector 天然承载两种粒度）。
func addressingLabelKey(name string) string {
	key := "fleetly.a." + sanitizeAliasName(name)
	// label key 上限 63（含前缀）；截断保前缀 + 哈希兜底（碰撞面由 Service
	// selector 全等匹配兜住，不同名截断同形会让两条 Service 选同一集——
	// 哈希后缀消除）。
	if len(key) > dnsLabelLimit {
		sum := sha256.Sum256([]byte(key))
		key = key[:dnsLabelLimit-9] + "-" + hex.EncodeToString(sum[:])[:8]
	}
	return key
}

// sanitizeAliasName 把别名压到 DNS 安全集（内点保形——多标签名
// {进程名}.{应用名} 是合法 k8s Service 名成分；swarm sanitizeAliasName
// 同源语义）。
func sanitizeAliasName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-.")
}

// nsSelector 是隔离域的 label 过滤器（域主体轴分支，与 workloadLabels
// 同构；对应 swarm nsSelector）。
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
	case ns.Browse != "":
		selector[labelBrowse] = sanitizeNamePart(ns.Browse)
	default:
		selector[labelApp] = sanitizeNamePart(ns.App)
	}
	return selector
}

// sortedKeys 返回 map 键的排序切片（map 遍历序随机，翻译路径凡 map →
// 切片的落点都必须经此归一——幂等 diff 逐字节稳定；守卫文化同 swarm）。
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// toContainer 把 Workload 的容器面翻译为 k8s 容器声明（Deployment/
// DaemonSet/Pod 共用）。
//
// 关键映射决策：
//   - 只配 readiness 探针、不配 liveness（k8s liveness 失败自动重启载体，
//     与平台语义自治冲突——L1 健康门/L2 看门狗在平台层；不健康在 Watch 流
//     呈现为 degraded，处置动作归平台，ADR-0052 决策 3）。
//   - Publish（host 与 mesh 模式）→ containerPort hostPort 直绑（ADR-0052
//     决策 5：k8s 无 routing mesh 对应物，宿主 IP 级发布原生原语）。
//   - SecretFiles → projected Secret 卷挂 /run/secrets/<名>（swarm secret
//     注入同语义；defaultMode 0444——非 root USER 镜像可读，staging 实证
//     口径）。
//   - HostBinds → hostPath 只读。
func toContainer(w capability.Workload, secretFiles map[string]string) corev1.Container {
	c := corev1.Container{
		Name:    "main",
		Image:   w.Image,
		Command: w.Command,
		Env:     envVars(w.Env),
	}
	for _, p := range w.Ports {
		c.Ports = append(c.Ports, corev1.ContainerPort{
			ContainerPort: p.Port,
			Protocol:      corev1.ProtocolTCP,
		})
	}
	// 宿主端口发布（受管域形态：Proxy 80/443、zot 5000、cadvisor 8080）。
	for _, pub := range w.Publish {
		c.Ports = append(c.Ports, corev1.ContainerPort{
			ContainerPort: pub.TargetPort,
			HostPort:      pub.PublishedPort,
			Protocol:      corev1.ProtocolTCP,
		})
	}
	if w.Resources != nil {
		limits := corev1.ResourceList{}
		if w.Resources.CPUMillis > 0 {
			limits[corev1.ResourceCPU] = resource.MustParse(
				strconv.FormatInt(w.Resources.CPUMillis, 10) + "m")
		}
		if w.Resources.MemoryMB > 0 {
			limits[corev1.ResourceMemory] = resource.MustParse(
				strconv.FormatInt(w.Resources.MemoryMB, 10) + "Mi")
		}
		c.Resources = corev1.ResourceRequirements{Limits: limits}
	}
	if hc := readinessProbe(w.Healthcheck); hc != nil {
		c.ReadinessProbe = hc
	}
	for _, v := range w.Volumes {
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name:      "vol-" + sanitizeNamePart(v.VolumeID),
			MountPath: v.Target,
			ReadOnly:  v.ReadOnly,
		})
	}
	for _, b := range w.HostBinds {
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name:      "bind-" + strconv.Itoa(len(c.VolumeMounts)),
			MountPath: b.Target,
			ReadOnly:  true,
		})
	}
	// 材料注入：单个 projected 卷挂 /run/secrets，逐 Secret 的 value 键经
	// items 投影为文件 <平台名>——与 docker secrets 语义逐位对齐（挂载点
	// 即文件本体；裸 Secret 卷挂 /run/secrets/<名> 会得到目录/<名>/value
	// 两级形态，模板 _FILE env 指到目录即崩——e2e db 段 CrashLoop 实证，
	// ADR-0014 的容器内路径契约）。
	if len(secretFiles) > 0 {
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name:      secretsVolumeName,
			MountPath: "/run/secrets",
			ReadOnly:  true,
		})
	}
	return c
}

// envVars 把 env map 翻译为排序的 EnvVar 列表（幂等 diff 稳定）。
func envVars(env map[string]string) []corev1.EnvVar {
	if len(env) == 0 {
		return nil
	}
	keys := sortedKeys(env)
	out := make([]corev1.EnvVar, 0, len(keys))
	for _, k := range keys {
		out = append(out, corev1.EnvVar{Name: k, Value: env[k]})
	}
	return out
}

// readinessProbe 把声明式探针翻译为 k8s readiness 探针（全解析 IR——
// http 端口/回退链在 engine 投影期解析，此处只做原语映射；k8s 原生三形态
// 无 shell 方言坑）。
func readinessProbe(h *capability.Healthcheck) *corev1.Probe {
	if h == nil {
		return nil
	}
	probe := &corev1.Probe{
		InitialDelaySeconds: int32(h.StartPeriod.Seconds()),
		PeriodSeconds:       int32(h.Interval.Seconds()),
		TimeoutSeconds:      int32(h.Timeout.Seconds()),
		FailureThreshold:    h.Retries,
	}
	switch {
	case h.HTTPPath != "":
		probe.HTTPGet = &corev1.HTTPGetAction{
			Path: h.HTTPPath,
			Port: intstr.FromInt32(h.HTTPPort),
		}
	case h.TCPPort != 0:
		probe.TCPSocket = &corev1.TCPSocketAction{
			Port: intstr.FromInt32(h.TCPPort),
		}
	case h.Exec != nil:
		probe.Exec = &corev1.ExecAction{Command: h.Exec}
	default:
		return nil
	}
	if probe.PeriodSeconds == 0 {
		probe.PeriodSeconds = 10 // k8s 缺省节律（零值 Interval 声明的诚实回退）
	}
	if probe.TimeoutSeconds == 0 {
		probe.TimeoutSeconds = 1
	}
	if probe.FailureThreshold == 0 {
		probe.FailureThreshold = 3
	}
	return probe
}

// secretsVolumeName 是材料 projected 卷的载体卷名（全部 SecretFiles 单卷
// 投影到 /run/secrets——docker secrets 语义对齐，见挂载处坑注）。
const secretsVolumeName = "fleetly-secrets"

// secretDataKey 是 ensureSecrets 落盘 Secret 的数据键（投影 items 的源键）。
const secretDataKey = "value"

// secretObjectName 是 Secret 材料的 k8s 对象名（值经 k8s Secret 分发——
// 不落载体 label 或明文 env，ADR-0014）。
func secretObjectName(platformName string) string {
	return "fleetly-mat-" + sanitizeNamePart(platformName)
}

// podTemplate 构造 Pod 模板（Deployment/DaemonSet 共用）。restart 由载体
// 类型承载（Deployment=Always 语义；one-shot Pod=Never）。
func podTemplate(ns capability.NamespaceRef, w capability.Workload, gen capability.Generation, secretFiles map[string]string, restart corev1.RestartPolicy) corev1.PodTemplateSpec {
	labels := workloadLabels(ns, w, gen)
	spec := corev1.PodSpec{
		Containers: []corev1.Container{toContainer(w, secretFiles)},
		// 载体不消费 k8s API：SA token 自动挂载关闭。双因：kubelet 的
		// kube-api-access 投影卷要在 /run/secrets/kubernetes.io 建挂载点，
		// 与材料 projected 卷的 /run/secrets 只读挂载冲突（runc "read-only
		// file system"、exit 128 起容器即炸——e2e db 段实证）；且是最小
		// 权限面——载体永不持有集群凭证。
		AutomountServiceAccountToken: ptr(false),
	}
	if restart != "" {
		spec.RestartPolicy = restart
	}
	if w.StopGrace > 0 {
		grace := int64(w.StopGrace.Seconds())
		if grace < 1 {
			grace = 1
		}
		spec.TerminationGracePeriodSeconds = &grace
	}
	if len(w.Placement.NodeIDs) > 0 {
		spec.NodeSelector = map[string]string{}
		for _, id := range w.Placement.NodeIDs {
			spec.NodeSelector[labelNodeID] = id // 多 ID 取末值：swarm 约束集是 OR 语义，k8s nodeSelector 是 AND——单值钉住是卷钉住的实质形态（多节点候选卷已钉单节点）
		}
	}
	for _, v := range w.Volumes {
		spec.Volumes = append(spec.Volumes, corev1.Volume{
			Name: "vol-" + sanitizeNamePart(v.VolumeID),
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName(v.VolumeID),
					ReadOnly:  v.ReadOnly,
				},
			},
		})
	}
	for _, b := range w.HostBinds {
		spec.Volumes = append(spec.Volumes, corev1.Volume{
			Name: "bind-" + strconv.Itoa(len(spec.Volumes)),
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: b.Source},
			},
		})
	}
	if len(secretFiles) > 0 {
		sources := make([]corev1.VolumeProjection, 0, len(secretFiles))
		for _, platformName := range sortedKeys(secretFiles) {
			sources = append(sources, corev1.VolumeProjection{
				Secret: &corev1.SecretProjection{
					// 解析集的域唯一对象名（ensureSecrets 已落盘的实体——重算
					// 常量名会与域唯一名分叉，投影引用即悬空）。
					LocalObjectReference: corev1.LocalObjectReference{Name: secretFiles[platformName]},
					Items: []corev1.KeyToPath{{
						Key:  secretDataKey,
						Path: platformName,
					}},
				},
			})
		}
		spec.Volumes = append(spec.Volumes, corev1.Volume{
			Name: secretsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources:     sources,
					DefaultMode: ptr(int32(0o444)),
				},
			},
		})
	}
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels},
		Spec:       spec,
	}
}

// ptr 是泛型取址助手（k8s API 大量指针字段）。
func ptr[T any](v T) *T { return &v }

// toDeployment 把长运行 Workload 翻译为 Deployment。
func toDeployment(ns capability.NamespaceRef, w capability.Workload, gen capability.Generation, secretFiles map[string]string, pullSecrets []string) *appsv1.Deployment {
	template := podTemplate(ns, w, gen, secretFiles, corev1.RestartPolicyAlways)
	for _, name := range pullSecrets {
		template.Spec.ImagePullSecrets = append(template.Spec.ImagePullSecrets,
			corev1.LocalObjectReference{Name: name})
	}
	replicas := int32(0)
	if w.Replicas > 0 {
		replicas = int32(w.Replicas) //nolint:gosec // 副本计数域内（平台配额远小于 2^31）
	} else if w.Replicas < 0 {
		replicas = 0
	}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   workloadName(ns, w, gen),
			Labels: template.Labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(replicas),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{labelWorkload: w.ID},
			},
			Template: template,
		},
	}
	// 挂卷/hostPort 负载 = Recreate 策略（单实例争用面：RWO 卷与宿主端口
	// 节点级排他——swarm rolloutOrder stop-first 的 k8s 等价物；无争用负载
	// 维持 k8s 缺省 RollingUpdate）。
	if len(w.Volumes) > 0 || hasHostPublish(w) {
		d.Spec.Strategy = appsv1.DeploymentStrategy{
			Type: appsv1.RecreateDeploymentStrategyType,
		}
	}
	return d
}

// toDaemonSet 把 Global 声明（受管采集面每节点一载体，ADR-0041）翻译为
// DaemonSet（Workload.Global 的 k8s 原语，架构 §5 契约注释的既定映射）。
func toDaemonSet(ns capability.NamespaceRef, w capability.Workload, gen capability.Generation, secretFiles map[string]string, pullSecrets []string) *appsv1.DaemonSet {
	template := podTemplate(ns, w, gen, secretFiles, corev1.RestartPolicyAlways)
	for _, name := range pullSecrets {
		template.Spec.ImagePullSecrets = append(template.Spec.ImagePullSecrets,
			corev1.LocalObjectReference{Name: name})
	}
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:   workloadName(ns, w, gen),
			Labels: template.Labels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{labelWorkload: w.ID},
			},
			Template: template,
		},
	}
}

// toOneShotPod 把 one-shot Run Workload（RestartNever，ADR-0025 决策 1：
// 退出即终态、补足由平台池语义承担）翻译为独立 Pod。
func toOneShotPod(ns capability.NamespaceRef, w capability.Workload, gen capability.Generation, secretFiles map[string]string, pullSecrets []string) *corev1.Pod {
	template := podTemplate(ns, w, gen, secretFiles, corev1.RestartPolicyNever)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   workloadName(ns, w, gen),
			Labels: template.Labels,
		},
		Spec: template.Spec,
	}
	for _, name := range pullSecrets {
		pod.Spec.ImagePullSecrets = append(pod.Spec.ImagePullSecrets,
			corev1.LocalObjectReference{Name: name})
	}
	return pod
}

// hasHostPublish 报告 Workload 是否声明 host 模式端口发布（swarm 同名
// 先例：判定不看 Global/Replicas 形态，端口排他性来自 host 直绑本身）。
func hasHostPublish(w capability.Workload) bool {
	for _, p := range w.Publish {
		if p.Mode == capability.PublishModeHost {
			return true
		}
	}
	return false
}

// serviceCarrierName 是 Addressing 平台名的 Service 载体名。k8s Service
// 名必须是单个 DNS label（RFC 1123，不允许点）——平台全名
// {进程名}.{应用名} 的点折横线（web.web → web-web；app 段消歧保留）。
// 裸名单 label 语义零变化；全名折点是 k3s 方言的诚实边界（Describe
// Notes 声明 + ADR-0052 决策 3 注记：跨 Runtime 的全名引用不保持，裸名
// 保持）。
func serviceCarrierName(addressing string) string {
	return sanitizeNamePart(addressing)
}

// toService 把一条平台 DNS 名声明翻译为 Service（selector = addressing
// label；池级名选全部声明载体（task-<id> 池 RR）、单载体名选自身——
// k8s endpoint 天然 RR = swarm alias RR 的原生等价）。ports 取该 Workload
// 的声明端口（Addresses 期望集端口注入语义同源）。labels 携域锚
// （serviceLabels：域收敛与 Remove 的对照面——Service 名是平台 Addressing
// 名的载体形态，不带 workload 轴）。无声明端口的工作负载 → headless
// （clusterIP None）：k8s 硬校验下零端口 Service 仅此形态合法（v1.36
// validation.go：ports required unless headless/ExternalName——e2e 实证
// portless worker 普通 Service 即 "spec.ports: Required value" 整拍 Ensure
// 炸）；名解析语义保持（headless = 直返 pod IP，单副本与 swarm 无差；
// 多副本无 VIP 轮询——Notes 诚实边界）。
func toService(ns capability.NamespaceRef, addressing string, selectorKey string, w capability.Workload) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   serviceCarrierName(addressing),
			Labels: serviceLabels(ns),
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{selectorKey: "true"},
		},
	}
	if len(w.Ports) == 0 {
		svc.Spec.ClusterIP = corev1.ClusterIPNone
		return svc
	}
	for _, p := range w.Ports {
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{
			Name:       "p" + strconv.Itoa(int(p.Port)),
			Port:       p.Port,
			TargetPort: intstr.FromInt32(p.Port),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	return svc
}

// toPVC 把 Volume 附件翻译为 PVC 声明（local-path StorageClass 由 k3s 自带
// 默认绑定——不显式指定 StorageClass，集群缺省即 local-path；卷钉住语义由
// local-path PV 的节点亲和天然承载，ADR-0052 决策 7）。
func toPVC(volumeID string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   pvcName(volumeID),
			Labels: map[string]string{labelManaged: "true"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(defaultVolumeStorage),
				},
			},
		},
	}
}

// toEgressNetpol 构造 egress 强隔离 NetworkPolicy（ADR-0052 决策 6：
// per-carrier 粒度——podSelector 选 fleetly.egress=true 载体；放行集 =
// 同 Namespace 流量 + kube-system DNS（UDP/TCP 53）；其余出站全拒。入站
// 方向不设规则（netpol 有状态回程自动放行，Proxy/域内消费不受影响）。
func toEgressNetpol() *networkingv1.NetworkPolicy {
	dns := networkingv1.NetworkPolicyPort{}
	udp := corev1.ProtocolUDP
	dns.Protocol = &udp
	dns.Port = ptr(intstr.FromInt32(dnsPort))
	dnsTCP := networkingv1.NetworkPolicyPort{}
	tcp := corev1.ProtocolTCP
	dnsTCP.Protocol = &tcp
	dnsTCP.Port = ptr(intstr.FromInt32(dnsPort))
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "fleetly-egress-deny",
			Labels: map[string]string{labelManaged: "true"},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{labelEgress: "true"},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					// 同 Namespace 全通（域内互通保留——swarm overlay 域内全通的
					// 对应物）。
					To: []networkingv1.NetworkPolicyPeer{
						{PodSelector: &metav1.LabelSelector{}},
					},
				},
				{
					// kube-system DNS（集群服务发现）。
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": dnsNamespace,
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{dns, dnsTCP},
				},
			},
		},
	}
}

// imagePullSecretName 是 registry 拉取凭证的 k8s Secret 对象名（server 地址
// 净化——Materials.RegistryAuth 的 server → imagePullSecrets，ADR-0014 分发
// 面的 k8s 原语）。
func imagePullSecretName(server string) string {
	return "fleetly-pull-" + sanitizeNamePart(server)
}

// fmt 包引用占位（错误文本构造在 runtime.go；保翻译层依赖最小）。
var _ = fmt.Sprintf
