package metrics

// 托管 metrics 三件套（VictoriaMetrics 单机版 + cAdvisor + node_exporter）
// 的期望 spec 构造与幂等比对（E6 观测专项设计 §4.1，W5-S3；D-W5-2 opt-in；
// internal/victorialogs spec 同款纪律）：不存在创建、存在比对（镜像/参数/
// 挂载/网络/约束/副本/限额/抓取配置引用）漂移即更新。
//
// 部署拓扑（设计 §4.1 + 一处**已记录的实现偏离**，D-W5-4 同族取证）：
//   - 设计字面 = VM host-mode 端口发布回环 8428 + cAdvisor/node_exporter 挂
//     内部 overlay 网络 `fleetly-metrics-net` + VM 抓 `tasks.fleetly-*`
//     （overlay DNS RR）。实现时点核实两处基座事实：
//     ①Engine API 对 swarm 服务端口的 HostIp 静默丢弃（W5-S1 dind 实证，
//     spec.go 头注记同源）——host-mode 发布绑 *:8428 = 公网暴露，回环不变
//     量不成立；②host 网络任务不能挂 overlay（S1 已实证）——VM 在宿主
//     网络命名空间内既解析不了 `tasks.*`（Docker 内嵌 DNS 不服务宿主）也
//     路由不到 overlay 任务 IP，抓取面为空。
//   - 落地 = **三件全部 host 网络任务 + 各自进程原生回环监听**（W5-S1
//     D-W5-4 的等价承载形态）：VM `-httpListenAddr=127.0.0.1:8428`、
//     cAdvisor `-listen_ip=127.0.0.1 -port=8080`、node_exporter
//     `--web.listen-address=127.0.0.1:9100`（flag 名逐一经官方镜像
//     -help 实测核实，2026-09-22）。回环不变量（零公网面）比设计字面更
//     严格地成立；VM 抓取 targets = 宿主回环 8080/9100（同命名空间直连）。
//   - **跨节点采集诚实边界**：回环 = 每节点各管各的，VM（钉 manager）只
//     采到 manager 节点序列；worker 节点指标缺席（global 任务在位但不被
//     抓）——`metrics status` / Console 以「N/M nodes reporting」如实
//     披露，不谎报。跨节点采集依赖 overlay 数据面（W3-F2）与 VM 访问面
//     的联合裁决，随设计修订票补（见 victorialogs spec 头注记的同族
//     「留位网络」处理——`fleetly-metrics-net` 本阶段不创建）。
//
// 抓取配置分发（设计 §4.1 的 `-prometheus.config` 内联形态不可实现——
// 实测 VM v1.152.0 无该 flag，单机版抓取配置 flag 是 **`-promscrape.
// config=<文件路径>`**，只吃文件/http URL 不吃内联 YAML；2026-09-22 镜像
// -help 取证）：落地 = **swarm config 对象**（内容寻址命名
// `fleetly-vm-scrape-<hash8>`，不可变；内容变更 = 新对象 + 服务 spec 引用
// 更新 + 旧对象 GC），挂载到任务内 `/etc/fleetly/vm-scrape.yml`。
//
// 数据安全语义（rustfs/victorialogs 同型）：mode=unset → 三件服务移除 +
// 旧抓取 config 清场；数据卷 **永不删除**（再启用复用）。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// 平台 metrics 常量（设计 §4.1 形态表；不走配置面）。
const (
	// VictoriaServiceName 是托管 VictoriaMetrics 单机版服务名。
	VictoriaServiceName = "fleetly-victoriametrics"
	// CAdvisorServiceName 是托管 cAdvisor 服务名（global——每节点一任务）。
	CAdvisorServiceName = "fleetly-cadvisor"
	// NodeExporterServiceName 是托管 node_exporter 服务名（global）。
	NodeExporterServiceName = "fleetly-node-exporter"
	// VolumeName 是 VM 数据本地命名卷（钉 manager——数据重力；禁用保留，
	// 再启用复用；VM 数据不进 state_backups，卷损 = 丢失保留窗内的指标，
	// 不损平台状态）。
	VolumeName = "fleetly-victoriametrics-data"
	// scrapeConfigPrefix 是抓取配置 swarm config 对象名前缀（后随
	// 内容 hash8——内容寻址，不可变对象按引用换版）。
	scrapeConfigPrefix = "fleetly-vm-scrape-"
	// scrapeLabel 是抓取配置对象的自描述 label（GC 选择器锚）。
	scrapeLabel = "fleetly.victoriametrics-scrape"
	// vmLabel / cadvisorLabel / nodeExporterLabel 是各服务的自描述 label
	//（CLI/运维识别面）。
	vmLabel           = "fleetly.victoriametrics"
	cadvisorLabel     = "fleetly.cadvisor"
	nodeExporterLabel = "fleetly.node-exporter"
	// dataMountPath 是 VM 数据目录挂载点（-storageDataPath 指向它）。
	dataMountPath = "/vmdata"
	// scrapeConfigMountPath 是抓取配置在任务内的挂载路径
	//（-promscrape.config 指向它）。
	scrapeConfigMountPath = "/etc/fleetly/vm-scrape.yml"
	// QueryPort 是 VM HTTP API 端口（VM 缺省 8428：query/ingest/health 同
	// 端口；host 网络 + 回环监听的目标地址）。
	QueryPort = 8428
	// CAdvisorPort / NodeExporterPort 是采集器的回环监听端口（cAdvisor
	// 官方缺省 8080、node_exporter 官方缺省 9100——端口未改只收编）。
	CAdvisorPort     = 8080
	NodeExporterPort = 9100
	// hostIP 是回环监听绑定地址（D-W5-4：127.0.0.1 = 零公网面）。
	hostIP = "127.0.0.1"
	// vmMemoryBytes 是 VM 内存限额起步值（设计 §4.1：128MB，实测校准门
	// 挂账——先测 idle 再定）。
	vmMemoryBytes = int64(128) << 20
	// cadvisorMemoryBytes 是 cAdvisor 内存限额起步值（设计 §4.1 注：128MB
	// 踩线、192MB 可接受——cAdvisor 是预算大头，限额保守但别 OOM 杀循环）。
	cadvisorMemoryBytes = int64(192) << 20
	// nodeExporterMemoryBytes 是 node_exporter 内存限额（idle RSS 极小，
	// 64MB 裕量充足）。
	nodeExporterMemoryBytes = int64(64) << 20

	// retryInterval 是 duty 收敛失败的退避缺省（victorialogs duty 同款注入缝）。
	retryInterval = 30 * time.Second
	// scanInterval 是已收敛后的漂移复检周期。
	scanInterval = 60 * time.Second
)

// DefaultVictoriaMetricsImage 是托管 VictoriaMetrics 单机版的钉定镜像
// （钉 release tag 而非 latest——R7；多架构 OCI index digest，amd64/arm64
// 通吃）。选版：2026-09-22 解析，v1.152.0 为实现时点最新稳定（2026-09-14
// 发布；v1.151.0 / v1.148.4 为老分支续版）。取证：`docker buildx imagetools
// inspect victoriametrics/victoria-metrics:v1.152.0` → Digest
// sha256:86ca5fdb…，台账 docs/runbooks/image-prepull.md #15。升级 = 镜像
// 钉版换版票（digest + 台账 + 回归），不自动追新。
const DefaultVictoriaMetricsImage = "victoriametrics/victoria-metrics:v1.152.0@sha256:86ca5fdb6d87d56ba047b044039019ba2bd9042b36e35f6ea34e437b6c825cef"

// DefaultCAdvisorImage 是托管 cAdvisor 的钉定镜像。**repo 勘误（台账 #17
// 同源记录）**：设计字面「docker.io 系 google/cadvisor」在 Docker Hub 已
// 标注 DEPRECATED（「New images will NOT be pushed. Please use
// gcr.io/cadvisor/cadvisor instead」——Hub repo 描述原文，2026-09-22 实测；
// google/cadvisor 最后镜像 v0.33.0 停在 2019 年）——官方多架构发布 repo 是
// `gcr.io/cadvisor/cadvisor`。选版：v0.55.1 为实现时点最新稳定（gcr.io
// tags/list 实测，v0.54.1 之上）；digest sha256:3de2bd52…（manifest list
// 多架构 index）。
const DefaultCAdvisorImage = "gcr.io/cadvisor/cadvisor:v0.55.1@sha256:3de2bd5203120b866d74a9b283b2ffb8ec382fbf9dc321814700c6ea6f44ec57"

// DefaultNodeExporterImage 是托管 node_exporter 的钉定镜像（prom/node-
// exporter 官方 repo；v1.12.1 为实现时点最新 stable，2026-07-14 发布；
// digest sha256:1b4e4438…，manifest list 多架构 index）。台账 #16。
const DefaultNodeExporterImage = "prom/node-exporter:v1.12.1@sha256:1b4e4438faca4dd7e001dd445d161a4a2091b0fededa84093b3a8dfeae1f1be0"

// HealthPath / query 路径常量（VM v1.152 单机版 HTTP API；/health 返回
// "OK"）。查询面：GET /api/v1/query_range（区间）与 /api/v1/query（瞬时）。
const (
	HealthPath = "/health"
	// QueryRangePath 是 PromQL 区间查询端点（SearchMetrics 消费）。
	QueryRangePath = "/api/v1/query_range"
	// QueryInstantPath 是 PromQL 瞬时查询端点（nodes_reporting 计数消费）。
	QueryInstantPath = "/api/v1/query"
)

// constraintFor 是 manager 钉定约束（victorialogs/rustfs/ingress 同公式：
// node.labels.<LabelNodeID> == <platformID>；本地重写避免适配器反向依赖，
// 公式由测试钉死）。只用于 VM（单写点钉 manager）；cAdvisor/node_exporter
// 是 global 服务——每节点一任务，无约束。
func constraintFor(platformNodeID string) string {
	return "node.labels." + state.LabelNodeID + " == " + platformNodeID
}

// retentionArg 把保留天数翻译为 VM 参数值（-retentionPeriod 对齐 config
// 键 metrics.retention_days；`d` 后缀天数形态——VM 接受 s/h/d/w/M/y 后缀，
// `d` 形态最直读；缺省回落 14d——DefaultRetentionDays 的抄送面）。
func retentionArg(days int) string {
	if days <= 0 {
		days = DefaultRetentionDays
	}
	return fmt.Sprintf("-retentionPeriod=%dd", days)
}

// scrapeConfigYAML 构造 VM 抓取配置（确定性渲染——内容寻址命名的哈希基）：
// 静态 targets = 宿主回环上的 cAdvisor/node_exporter（同命名空间直连；
// 跨节点采集的诚实边界见文件头注记）。
func scrapeConfigYAML() string {
	return `global:
  scrape_interval: 15s
scrape_configs:
  - job_name: fleetly-cadvisor
    static_configs:
      - targets: ["` + loopbackTarget(CAdvisorPort) + `"]
  - job_name: fleetly-node-exporter
    static_configs:
      - targets: ["` + loopbackTarget(NodeExporterPort) + `"]
`
}

// loopbackTarget 渲染 host:port 回环目标（scrape 配置与 spec 注释同锚）。
func loopbackTarget(port int) string {
	return fmt.Sprintf("%s:%d", hostIP, port)
}

// scrapeConfigName 是抓取配置的 swarm config 对象名（内容寻址：内容变 =
// 名变 = 新对象；服务 spec 以名引用，specEqual 比对捕获漂移）。
func scrapeConfigName() string {
	sum := sha256.Sum256([]byte(scrapeConfigYAML()))
	return scrapeConfigPrefix + hex.EncodeToString(sum[:4])
}

// buildScrapeConfigSpec 构造抓取配置的期望 swarm.ConfigSpec。
func buildScrapeConfigSpec() swarm.ConfigSpec {
	return swarm.ConfigSpec{
		Annotations: swarm.Annotations{
			Name: scrapeConfigName(),
			Labels: map[string]string{
				state.LabelManaged: state.ManagedLabelValue,
				scrapeLabel:        "true",
			},
		},
		Data: []byte(scrapeConfigYAML()),
	}
}

// buildVictoriaSpec 构造托管 VictoriaMetrics 服务的期望 swarm spec
// （replicated-1 + manager 约束 + host 网络任务（回环监听）+ 数据卷 +
// 抓取配置引用 + 内存限额 128MB；无端口发布面——host 网络任务的进程自绑
// 127.0.0.1，EndpointSpec 恒空）。
func buildVictoriaSpec(platformID string, retentionDays int) swarm.ServiceSpec {
	one := uint64(1)
	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Name: VictoriaServiceName,
			Labels: map[string]string{
				state.LabelManaged: state.ManagedLabelValue,
				vmLabel:            "true",
			},
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image: DefaultVictoriaMetricsImage,
				Args: []string{
					"-storageDataPath=" + dataMountPath,
					retentionArg(retentionDays),
					fmt.Sprintf("-httpListenAddr=%s:%d", hostIP, QueryPort),
					"-promscrape.config=" + scrapeConfigMountPath,
				},
				Mounts: []mount.Mount{
					{Type: mount.TypeVolume, Source: VolumeName, Target: dataMountPath},
				},
				Configs: []*swarm.ConfigReference{{
					ConfigName: scrapeConfigName(),
					File: &swarm.ConfigReferenceFileTarget{
						Name: scrapeConfigMountPath,
						UID:  "0", GID: "0", Mode: 0o444,
					},
				}},
			},
			// host 网络：任务共享宿主网络命名空间，-httpListenAddr 把监听
			// 面收缩到宿主回环（零公网面）；抓取 targets 同命名空间回环
			// 直连（跨节点边界见文件头注记）。
			Networks: []swarm.NetworkAttachmentConfig{{
				Target: hostNetworkName,
			}},
			Placement: &swarm.Placement{
				Constraints: []string{constraintFor(platformID)},
			},
			Resources: &swarm.ResourceRequirements{
				Limits: &swarm.Limit{MemoryBytes: vmMemoryBytes},
			},
		},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &one}},
		UpdateConfig: &swarm.UpdateConfig{
			Parallelism:   1,
			FailureAction: "pause",
			Order:         "stop-first",
		},
	}
	return spec
}

// buildCAdvisorSpec 构造托管 cAdvisor 的期望 spec（global——每节点一任务；
// host 网络 + 回环监听（零公网面，`-listen_ip` 经官方镜像 -help 实测核实）
// + 官方容器形态的宿主只读挂载 + 限额 192MB；无 overlay——跨节点边界见
// 文件头注记）。
func buildCAdvisorSpec() swarm.ServiceSpec {
	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Name: CAdvisorServiceName,
			Labels: map[string]string{
				state.LabelManaged: state.ManagedLabelValue,
				cadvisorLabel:      "true",
			},
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image: DefaultCAdvisorImage,
				// 经 /bin/sh -c 自适应启动（2026-09-22 staging 实证补丁）：
				// containerd socket 的宿主形态有两种——dockerd 自管（dind：
				// /var/run/docker/containerd/containerd.sock）与系统 containerd
				//（多数宿主：/var/run/containerd/containerd.sock）。单值 flag
				// 无法在一枚 global spec 里覆盖两种宿主，启动期按存在性择一
				//（/var/run 已只读挂载，两种路径容器内均可见——staging 实证：
				// 系统形态 socket 在、私有路径缺；dind 恰反）。镜像基于 alpine
				// 带 /bin/sh（健康检查 CMD-SHELL 同依赖）。
				Command: []string{"/bin/sh", "-c"},
				Args: []string{
					`exec /usr/bin/cadvisor -logtostderr -listen_ip=` + hostIP +
						` -port=` + strconv.Itoa(CAdvisorPort) +
						// 只报 docker 容器（+root）——cgroup 序列基数与
						// housekeeping 负载的保守化（预算门 §4.3 先手减载；
						// 平台全部负载都是 docker 容器，无序列损失）+ moby
						// 命名空间（dockerd 的容器都在 moby，cAdvisor 缺省读
						// k8s.io）。
						` -docker_only -containerd="$([ -S /var/run/docker/containerd/containerd.sock ] && echo /var/run/docker/containerd/containerd.sock || echo /var/run/containerd/containerd.sock)"` +
						` -containerd-namespace=moby`,
				},
				Mounts: []mount.Mount{
					bindRO("/", "/rootfs"),
					bindRO("/var/run", "/var/run"),
					bindRO("/sys", "/sys"),
					bindRO("/var/lib/docker", "/var/lib/docker"),
				},
				// 显式健康检查（覆盖镜像自带的 localhost 探针——镜像缺省
				// wget 在双栈解析下走 ::1，而 -listen_ip=127.0.0.1 只绑 IPv4
				// 回环 → 探针恒败 → swarm 按 unhealthy 杀任务重启循环；
				// 2026-09-22 dind 实证。显式 127.0.0.1 探针同一不变量）。
				Healthcheck: &container.HealthConfig{
					Test: []string{
						"CMD-SHELL",
						fmt.Sprintf("wget --quiet --tries=1 --spider http://%s:%d/healthz || exit 1", hostIP, CAdvisorPort),
					},
					Interval:    30 * time.Second,
					Timeout:     3 * time.Second,
					StartPeriod: 10 * time.Second,
					Retries:     3,
				},
			},
			Networks: []swarm.NetworkAttachmentConfig{{
				Target: hostNetworkName,
			}},
			Resources: &swarm.ResourceRequirements{
				Limits: &swarm.Limit{MemoryBytes: cadvisorMemoryBytes},
			},
		},
		Mode: swarm.ServiceMode{Global: &swarm.GlobalService{}},
	}
}

// buildNodeExporterSpec 构造托管 node_exporter 的期望 spec（global；host
// 网络 + 回环监听 + 官方容器形态的宿主只读挂载与 --path.* 参数 + 限额
// 64MB）。
func buildNodeExporterSpec() swarm.ServiceSpec {
	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Name: NodeExporterServiceName,
			Labels: map[string]string{
				state.LabelManaged: state.ManagedLabelValue,
				nodeExporterLabel:  "true",
			},
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image: DefaultNodeExporterImage,
				Args: []string{
					"--path.rootfs=/host",
					"--path.procfs=/host/proc",
					"--path.sysfs=/host/sys",
					fmt.Sprintf("--web.listen-address=%s:%d", hostIP, NodeExporterPort),
				},
				Mounts: []mount.Mount{
					bindRO("/", "/host"),
					bindRO("/proc", "/host/proc"),
					bindRO("/sys", "/host/sys"),
				},
			},
			Networks: []swarm.NetworkAttachmentConfig{{
				Target: hostNetworkName,
			}},
			Resources: &swarm.ResourceRequirements{
				Limits: &swarm.Limit{MemoryBytes: nodeExporterMemoryBytes},
			},
		},
		Mode: swarm.ServiceMode{Global: &swarm.GlobalService{}},
	}
}

// bindRO 是宿主只读 bind 挂载的构造器（三件 spec 共用）。
func bindRO(source, target string) mount.Mount {
	return mount.Mount{Type: mount.TypeBind, Source: source, Target: target, ReadOnly: true}
}

// specEqual 幂等比对（镜像/参数/挂载/网络/约束/副本/限额/抓取配置引用
// ——服务的全部执行面都由期望 spec 权威表达；label 不参与，服务名即身份。
// 参数含 -httpListenAddr / -listen_ip / --web.listen-address 回环监听——
// 零公网面不变量漂移必被本比对捕获）。
func specEqual(cur ServiceState, desired swarm.ServiceSpec) bool {
	cs := desired.TaskTemplate.ContainerSpec
	if cur.Image != cs.Image {
		return false
	}
	if !sameStrings(cur.Args, cs.Args) {
		return false
	}
	if len(cur.MountSources) != len(cs.Mounts) {
		return false
	}
	for i, wm := range cs.Mounts {
		if cur.MountSources[i] != wm.Source || cur.MountTargets[i] != wm.Target {
			return false
		}
	}
	wantNets := make([]string, 0, len(desired.TaskTemplate.Networks))
	for _, n := range desired.TaskTemplate.Networks {
		wantNets = append(wantNets, n.Target)
	}
	if !sameStrings(cur.Networks, wantNets) {
		return false
	}
	wantConstraints := []string{}
	if pl := desired.TaskTemplate.Placement; pl != nil {
		wantConstraints = pl.Constraints
	}
	if !sameStrings(cur.Constraints, wantConstraints) {
		return false
	}
	// 抓取配置引用（内容寻址名——scrape 配置漂移经服务 spec 比对收敛）。
	wantConfigs := make([]string, 0, len(cs.Configs))
	for _, c := range cs.Configs {
		wantConfigs = append(wantConfigs, c.ConfigName)
	}
	if !sameStrings(cur.ConfigNames, wantConfigs) {
		return false
	}
	// 健康检查（执行面——探针序列漂移必捕获；nil 与空序列视为同形）。
	wantHealth := []string(nil)
	if cs.Healthcheck != nil {
		wantHealth = cs.Healthcheck.Test
	}
	if !sameStrings(cur.HealthTest, wantHealth) {
		return false
	}
	// 副本形态：replicated 比数值；global 比形态（Replicas=0 且 global）。
	wantGlobal := desired.Mode.Global != nil
	if cur.Global != wantGlobal {
		return false
	}
	if !wantGlobal {
		wantReplicas := uint64(0)
		if desired.Mode.Replicated != nil && desired.Mode.Replicated.Replicas != nil {
			wantReplicas = *desired.Mode.Replicated.Replicas
		}
		if cur.Replicas != wantReplicas {
			return false
		}
	}
	wantMem := int64(0)
	if res := desired.TaskTemplate.Resources; res != nil && res.Limits != nil {
		wantMem = res.Limits.MemoryBytes
	}
	return cur.MemoryBytes == wantMem
}

// sameStrings 序列相等（顺序敏感——spec 各面以期望序权威表达）。
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
