package engine

// 规划层（期望态构建）：归一化 compose + 放置裁决 + env 三层合并 + 镜像
// digest + 卷注册表 → []ServiceSpec 与期望态哈希。受管字段与平台缺省在此
// 固定（release-semantics §2.8）：
//   - failure_action=pause / monitor=5s（适配器固定补齐，本层不管）；
//   - order：compose 声明照用；有卷 / global 强制 stop-first（显式
//     start-first 的冲突已在校验层拒绝）；
//   - healthcheck 子字段缺省 5s/3s/3/10s；无 healthcheck = health_gate=none
//     （W_DEPLOY_NO_HEALTHCHECK 由 compose.Load 警告承载）；
//   - restart_policy 缺省 condition=any / delay=5s；
//   - parallelism 缺省 1（swarm 语义 0=不限流，必须显式落 1）。

import (
	"sort"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/envlayer"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/placement"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// 平台缺省健康检查参数（release-semantics §2.8 治理参数表：未写子字段取
// 平台默认 5s/3s/3/10s）。
const (
	defaultHealthInterval    = 5 * time.Second
	defaultHealthTimeout     = 3 * time.Second
	defaultHealthRetries     = 3
	defaultHealthStartPeriod = 10 * time.Second

	// defaultParallelism 是更新并行度缺省（compose 缺省 1；swarm 0=无限）。
	defaultParallelism = uint64(1)
)

// PlanInput 是一次期望态规划的输入（preparing 阶段收集齐）。
type PlanInput struct {
	AppID   string
	AppName string
	// DeploymentID 是发布归属（服务 label fleetly.deployment）。
	DeploymentID string
	// Spec 是归一化 compose（daemon 侧重载，二次校验防御）。
	Spec *compose.Spec
	// FileEnv / ComposeEnv 是 services.* 的文件层明文（extractServiceEnvs）。
	FileEnv    map[string]map[string]string
	ComposeEnv map[string]map[string]string
	// PlatformEnv 是已生效（effective）平台 env（三层合并的第三层输入；
	// pending 不参与合并——「随下次部署生效」由部署成功后的 promote 承载）。
	PlatformEnv []envlayer.PlatformVar
	// Images 是服务 → digest 钉定镜像引用（building 阶段产出）。
	Images map[string]string
	// Decision 是放置裁决（绑定约束编译结果）。
	Decision placement.Decision
	Volumes  []state.Volume
}

// Plan 是一次期望态规划产出。
type Plan struct {
	// Services 按服务名字典序（对账与快照确定性）。
	Services []ServiceSpec
	// EnvSnapshotHash 是 env 三层合并结果快照哈希（key:sha256+来源；
	// 值明文永不进哈希输入）。
	EnvSnapshotHash string
	// DesiredHash 是部署级期望态哈希（spec + env + 各服务哈希合成）。
	DesiredHash string
	// DesiredSpecJSON 是 []ServiceSpec 的 canonical JSON（密文化的快照明文
	// 形态——归位/重放的执行依据）。
	DesiredSpecJSON []byte
	// Warnings 是规划期警告（W_ENV_PLATFORM_OVERRIDE 等）。
	Warnings []compose.Warning
}

// BuildPlan 执行期望态规划。失败（服务无镜像/卷无登记/命名非法）返回
// apperr 信封错误。
func BuildPlan(in PlanInput) (*Plan, error) {
	volByKey := map[string]state.Volume{}
	for _, v := range in.Volumes {
		volByKey[v.Key] = v
	}

	plan := &Plan{}
	services := make([]ServiceSpec, 0, len(in.Spec.Services))
	envByService := map[string][]envSnapshotEntry{}
	for i := range in.Spec.Services {
		svc := &in.Spec.Services[i]
		image, ok := in.Images[svc.Name]
		if !ok || image == "" {
			return nil, errorf("E_BUILD_FAILED", "服务 %s 缺少镜像引用（构建/直通阶段未产出）", svc.Name)
		}
		spec, merged, err := buildServiceSpec(in, svc, image, volByKey)
		if err != nil {
			return nil, err
		}
		services = append(services, spec)
		envByService[svc.Name] = envEntriesOf(merged)
		plan.Warnings = append(plan.Warnings, envlayer.PlatformOverrideWarnings(&compose.Spec{
			Name:     in.Spec.Name,
			Services: []compose.Service{*svc},
		}, in.PlatformEnv)...)
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	plan.Services = services
	plan.EnvSnapshotHash = canonicalHash(envByService)

	// 快照与期望态哈希先于服务 label 附加（label 含部署 ID——不进哈希与
	// 快照；归位重放时由对账层以当前发布归属重写服务 label，任务零替换）。
	desiredJSON, err := canonicalJSON(services)
	if err != nil {
		return nil, errorf("E_RUNTIME_UNAVAILABLE", "期望态序列化失败: %v", err)
	}
	for i := range services {
		if services[i].ServiceLabels == nil {
			services[i].ServiceLabels = map[string]string{}
		}
		services[i].ServiceLabels[state.LabelDesiredHash] = services[i].DesiredHash()
	}
	plan.DesiredSpecJSON = desiredJSON

	// 部署级哈希：spec + env + 各服务哈希。
	type desiredSummary struct {
		SpecHash        string            `json:"spec_hash"`
		EnvSnapshotHash string            `json:"env_snapshot_hash"`
		Services        map[string]string `json:"services"`
	}
	summary := desiredSummary{
		SpecHash:        in.Spec.SpecHash,
		EnvSnapshotHash: plan.EnvSnapshotHash,
		Services:        map[string]string{},
	}
	for i := range services {
		summary.Services[services[i].Name] = services[i].DesiredHash()
	}
	plan.DesiredHash = canonicalHash(summary)
	return plan, nil
}

// buildServiceSpec 规划单个服务（返回 spec 与合并结果；spec 此时不带服务
// label——调用方统一附加）。
func buildServiceSpec(in PlanInput, svc *compose.Service, image string, volByKey map[string]state.Volume) (ServiceSpec, []envlayer.Merged, error) {
	swarmName, err := naming.ServiceName(in.AppName, svc.Name)
	if err != nil {
		return ServiceSpec{}, nil, errorf("E_RUNTIME_UNAVAILABLE", "服务 %s 命名失败: %v", svc.Name, err)
	}
	alias, err := naming.NetworkAlias(svc.Name)
	if err != nil {
		return ServiceSpec{}, nil, errorf("E_RUNTIME_UNAVAILABLE", "服务 %s 别名失败: %v", svc.Name, err)
	}
	netName, err := naming.NetworkName(in.AppName)
	if err != nil {
		return ServiceSpec{}, nil, errorf("E_RUNTIME_UNAVAILABLE", "应用 %s 网络命名失败: %v", in.AppName, err)
	}

	// env 三层合并（文件层明文 × effective 平台层）。
	fileEnv := in.FileEnv[svc.Name]
	composeEnv := in.ComposeEnv[svc.Name]
	if fileEnv == nil {
		fileEnv = map[string]string{}
	}
	if composeEnv == nil {
		composeEnv = map[string]string{}
	}
	merged, _ := envlayer.MergeChain(fileEnv, composeEnv, in.PlatformEnv)
	envList := make([]string, 0, len(merged))
	for _, m := range merged {
		envList = append(envList, m.Key+"="+m.Value)
	}

	// 卷挂载：compose 卷 key → 卷注册表 docker 名。
	var mounts []MountSpec
	for _, m := range svc.Volumes {
		vol, ok := volByKey[m.Volume]
		if !ok || vol.Name == "" {
			return ServiceSpec{}, nil, errorf("E_PLACEMENT_NODE_UNAVAILABLE",
				"服务 %s 的卷 %s 未在卷注册表登记（放置 Apply 先于规划执行）", svc.Name, m.Volume)
		}
		mounts = append(mounts, MountSpec{VolumeName: vol.Name, Target: m.Target, ReadOnly: m.ReadOnly})
	}

	// secret 引用：v0.1 平台密钥库未接入（平台密钥存储随后续票）——显式
	// 快速失败，不静默丢引用。
	if len(svc.Secrets) > 0 {
		return ServiceSpec{}, nil, errorf("E_RUNTIME_UNAVAILABLE",
			"服务 %s 声明了 secrets %s：平台密钥库 v0.1 未接入（密钥存储与 Swarm secret 下发随后续票落地）",
			svc.Name, strings.Join(svc.Secrets, ", "))
	}

	// 更新顺序：有卷 / global 强制 stop-first；其余 compose 声明照用（缺省
	// start-first）。
	order := composeOrder(svc)
	if len(mounts) > 0 || svc.Deploy != nil && svc.Deploy.Mode == "global" {
		order = "stop-first"
	}

	spec := ServiceSpec{
		Name:    swarmName,
		Image:   image,
		Command: append([]string{}, svc.Command...),
		Env:     envList,
		ContainerLabels: map[string]string{
			state.LabelApp: in.AppName,
		},
		ServiceLabels:     namingServiceLabels(in.AppName, svc.Name, in.DeploymentID),
		Global:            svc.Deploy != nil && svc.Deploy.Mode == "global",
		Replicas:          composeReplicas(svc),
		Networks:          []NetworkAttach{{Name: netName, Aliases: []string{alias}}},
		Mounts:            mounts,
		Healthcheck:       composeHealthcheck(svc.Healthcheck),
		UpdateOrder:       order,
		UpdateParallelism: composeParallelism(svc),
		UpdateDelay:       composeDelay(svc),
		RestartPolicy:     composeRestartPolicy(svc),
		Resources:         composeResources(svc),
		StopSignal:        svc.StopSignal,
		StopGracePeriod:   composeStopGrace(svc),
	}
	// 放置约束：绑定钉住编译 + compose 声明（node.labels.fleetly.* 命名空间）。
	if in.Decision.Bind && in.Decision.Constraint != "" && len(mounts) > 0 {
		spec.Constraints = append(spec.Constraints, in.Decision.Constraint)
	}
	if svc.Deploy != nil && svc.Deploy.Placement != nil {
		spec.Constraints = append(spec.Constraints, svc.Deploy.Placement.Constraints...)
	}
	return spec, merged, nil
}

// namingServiceLabels 构造服务 label 最小集 + 部署归属（naming 失败视为
// 不可达的命名违约——受控子集已保证字符集）。
func namingServiceLabels(app, service, deploymentID string) map[string]string {
	labels, err := naming.ServiceLabels(app, service, deploymentID)
	if err != nil {
		return map[string]string{
			state.LabelManaged:    state.ManagedLabelValue,
			state.LabelApp:        app,
			state.LabelProcess:    service,
			state.LabelDeployment: deploymentID,
		}
	}
	return labels
}

// envSnapshotEntry 是 env 快照的脱敏条目（key:sha256+来源；值明文与长度
// 都不进输入）。
type envSnapshotEntry struct {
	Key    string `json:"key"`
	Hash   string `json:"hash"`
	Source string `json:"source"`
}

// envEntriesOf 投影合并结果为快照条目。
func envEntriesOf(merged []envlayer.Merged) []envSnapshotEntry {
	entries := make([]envSnapshotEntry, 0, len(merged))
	for _, m := range merged {
		entries = append(entries, envSnapshotEntry{Key: m.Key, Hash: m.Hash, Source: string(m.Source)})
	}
	return entries
}

// composeVolumes 汇总归一化 spec 的命名卷挂载（放置层的输入；key 去重）。
func composeVolumes(spec *compose.Spec) []placement.VolumeMount {
	seen := map[string]bool{}
	var out []placement.VolumeMount
	for i := range spec.Services {
		for _, m := range spec.Services[i].Volumes {
			if seen[m.Volume] {
				continue
			}
			seen[m.Volume] = true
			out = append(out, placement.VolumeMount{Key: m.Volume, Target: m.Target, ReadOnly: m.ReadOnly})
		}
	}
	return out
}

// composePlacementLabel 提取放置意图 label（受控子集校验已保证跨服务一致；
// 取首个非空）。
func composePlacementLabel(spec *compose.Spec) string {
	for i := range spec.Services {
		if spec.Services[i].PlacementNode != "" {
			return spec.Services[i].PlacementNode
		}
	}
	return ""
}

// envKeySetsMatch 交叉核对提取器与归一化形态的 env 键集（防 compose 文件
// 在入队后被并发改写——键集漂移即拒绝）。
func envKeySetsMatch(specEnv []compose.EnvVar, fileEnv, composeEnv map[string]string) bool {
	union := map[string]bool{}
	for k := range fileEnv {
		union[k] = true
	}
	for k := range composeEnv {
		union[k] = true
	}
	if len(union) != len(specEnv) {
		return false
	}
	for _, e := range specEnv {
		if !union[e.Key] {
			return false
		}
	}
	return true
}

// composeOrder 读 compose 声明的更新顺序（缺省 start-first）。
func composeOrder(svc *compose.Service) string {
	if svc.Deploy != nil && svc.Deploy.UpdateConfig != nil && svc.Deploy.UpdateConfig.Order != "" {
		return svc.Deploy.UpdateConfig.Order
	}
	return "start-first"
}

// composeReplicas 读期望副本（缺省 1；global 模式忽略）。
func composeReplicas(svc *compose.Service) uint64 {
	if svc.Deploy != nil && svc.Deploy.Replicas > 0 {
		return uint64(svc.Deploy.Replicas)
	}
	return 1
}

// composeParallelism 读更新并行度（缺省 1——swarm 0=不限流，必须显式落 1）。
func composeParallelism(svc *compose.Service) uint64 {
	if svc.Deploy != nil && svc.Deploy.UpdateConfig != nil && svc.Deploy.UpdateConfig.Parallelism > 0 {
		return svc.Deploy.UpdateConfig.Parallelism
	}
	return defaultParallelism
}

// composeDelay 读更新间隔。
func composeDelay(svc *compose.Service) time.Duration {
	if svc.Deploy != nil && svc.Deploy.UpdateConfig != nil && svc.Deploy.UpdateConfig.Delay != "" {
		if d, err := time.ParseDuration(svc.Deploy.UpdateConfig.Delay); err == nil {
			return d
		}
	}
	return 0
}

// composeHealthcheck 翻译健康检查（未写子字段取平台缺省 5s/3s/3/10s；
// nil = health_gate=none）。
func composeHealthcheck(hc *compose.Healthcheck) *HealthcheckSpec {
	if hc == nil || len(hc.Test) == 0 {
		return nil
	}
	out := &HealthcheckSpec{
		Test:        append([]string{}, hc.Test...),
		Interval:    defaultHealthInterval,
		Timeout:     defaultHealthTimeout,
		Retries:     defaultHealthRetries,
		StartPeriod: defaultHealthStartPeriod,
	}
	if hc.Interval != "" {
		if d, err := time.ParseDuration(hc.Interval); err == nil {
			out.Interval = d
		}
	}
	if hc.Timeout != "" {
		if d, err := time.ParseDuration(hc.Timeout); err == nil {
			out.Timeout = d
		}
	}
	if hc.Retries > 0 {
		out.Retries = hc.Retries
	}
	if hc.StartPeriod != "" {
		if d, err := time.ParseDuration(hc.StartPeriod); err == nil {
			out.StartPeriod = d
		}
	}
	return out
}

// composeRestartPolicy 翻译重启策略（缺省 condition=any / delay=5s）。
func composeRestartPolicy(svc *compose.Service) *RestartPolicySpec {
	out := &RestartPolicySpec{Condition: string(conditionAny), Delay: defaultRestartDelay}
	if svc.Deploy != nil && svc.Deploy.RestartPolicy != nil {
		rp := svc.Deploy.RestartPolicy
		if rp.Condition != "" {
			out.Condition = rp.Condition
		}
		if rp.Delay != "" {
			if d, err := time.ParseDuration(rp.Delay); err == nil {
				out.Delay = d
			}
		}
		out.MaxAttempts = rp.MaxAttempts
		if rp.Window != "" {
			if d, err := time.ParseDuration(rp.Window); err == nil {
				out.Window = d
			}
		}
	}
	return out
}

// conditionAny 与适配器缺省保持同一字面值。
const conditionAny = "any"

// defaultRestartDelay 是重启策略缺省间隔（architecture §2.5 运行期语义）。
const defaultRestartDelay = 5 * time.Second

// composeResources 翻译资源限额（仅 limits；cpus → nano CPUs）。
func composeResources(svc *compose.Service) *ResourcesSpec {
	if svc.Deploy == nil || svc.Deploy.Resources == nil || svc.Deploy.Resources.Limits == nil {
		return nil
	}
	limits := svc.Deploy.Resources.Limits
	out := &ResourcesSpec{}
	if limits.CPUS > 0 {
		out.NanoCPUs = int64(limits.CPUS * 1e9)
	}
	if limits.MemoryBytes > 0 {
		out.MemoryBytes = limits.MemoryBytes
	}
	if out.NanoCPUs == 0 && out.MemoryBytes == 0 {
		return nil
	}
	return out
}

// composeStopGrace 读停止宽限（≤0 = 底座缺省）。
func composeStopGrace(svc *compose.Service) time.Duration {
	if svc.StopGracePeriod == "" {
		return 0
	}
	if d, err := time.ParseDuration(svc.StopGracePeriod); err == nil {
		return d
	}
	return 0
}
