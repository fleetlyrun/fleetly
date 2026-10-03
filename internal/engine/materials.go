package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// registrySecretPrefix 是镜像凭证 Secret 的命名约定（registry:<host>；
// ADR-0014：凭证存 Secret，Ensure 解析后按节点分发）。
const registrySecretPrefix = "registry:"

// managedEndpoint 给受管仓库端点解析补独立硬界（B15-2）：Endpoint 是
// registry Provider 的外部调用，可独立 hang——序列级界（materialize 序列
// 头 / ensureTaskWorkloads 下发段）虽覆盖既有消费面，但 hang 会吃光整段
// 预算让 Ensure 饿死；独立界让端点解析先失败，预算留给后续步。双重
// WithTimeout 取 min，语义不变（drift.go 既有形态）。
func (e *Engine) managedEndpoint(ctx context.Context) (capability.RegistryEndpoint, error) {
	ctx, cancel := e.boundedStep(ctx)
	defer cancel()
	return e.registry.Endpoint(ctx)
}

// registryCredentialJSON 是 registry Secret 值的结构。
type registryCredentialJSON struct {
	Server   string `json:"server"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

// resolveMaterials 装配 Ensure 材料（ADR-0014：平台已解析后随 Ensure 下发；
// 值不落 Spec/日志——Revision 只冻结引用）。Build 声明存在的 spec 额外
// 携带受管仓库 host（from_build 下发引用的拉取凭证面，附录 B.3 分发面③）。
func (e *Engine) resolveMaterials(ctx context.Context, spec *specv1.AppSpec, projectID string) (capability.Materials, error) {
	refs := collectSecretRefs(spec)
	hosts := collectImageHosts(spec)
	if spec.GetBuild() != nil && e.registry != nil {
		endpoint, err := e.managedEndpoint(ctx)
		if err != nil {
			return capability.Materials{}, fmt.Errorf("resolve managed registry endpoint: %w", err)
		}
		if endpoint.Addr != "" && !contains(hosts, endpoint.Addr) {
			hosts = append(hosts, endpoint.Addr)
		}
	}
	return e.materialsFor(ctx, refs, hosts, projectID)
}

// materialsForProcess 装配单进程材料面（Task 域消费：TaskSpec.process 的
// secret_refs + 镜像 host 凭证——与 App 域同一条解析通道，单一真源）。
func (e *Engine) materialsForProcess(ctx context.Context, p *specv1.ProcessSpec, projectID string) (capability.Materials, error) {
	seen := map[string]bool{}
	refs := make([]string, 0, len(p.GetSecretRefs()))
	for _, ref := range p.GetSecretRefs() {
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	hosts := []string(nil)
	if img := p.GetImage(); img != "" {
		if host := capability.ImageRegistryHost(img); host != "" {
			hosts = []string{host}
		}
	}
	return e.materialsFor(ctx, refs, hosts, projectID)
}

// materialsFor 是材料解析的共用实现（registry 凭证 + Secret 文件注入）。
// 受管仓库 host 的凭证由平台直接注入（managed host 上项目级 Secret
// registry:<host> 不参与——平台凭证是唯一真源，ADR-0019 附录 B.3）。
func (e *Engine) materialsFor(ctx context.Context, refs, hosts []string, projectID string) (capability.Materials, error) {
	materials := capability.Materials{}
	if len(refs) > 0 && e.cipher == nil {
		return materials, fmt.Errorf("secret refs present but the master key is not available (data root keys/ missing)")
	}

	// 受管仓库端点（managed host 判定 + 平台凭证来源）。无 Registry =
	// 无平台凭证面（build 源部署已在 prepare 前置门精确失败）。
	var managed *capability.RegistryEndpoint
	if e.registry != nil {
		endpoint, err := e.managedEndpoint(ctx)
		if err != nil {
			return materials, fmt.Errorf("resolve managed registry endpoint: %w", err)
		}
		if endpoint.Addr != "" {
			managed = &endpoint
		}
	}

	// 镜像凭证：镜像引用的 registry host → Secret "registry:<host>"。查询
	// 错误分诊（Q-8）：ErrNotFound = 无凭证，匿名拉取是合法形态；其余错误
	// （库故障等）上抛部署失败带原因——静默降级匿名拉取会让私有镜像部署
	// 死在无诊断的 ImagePullBackOff 上。
	for _, host := range hosts {
		if managed != nil && host == managed.Addr {
			// 平台仓库：平台凭证直注（三面同源的分发面③）。
			if materials.RegistryAuth == nil {
				materials.RegistryAuth = map[string]capability.RegistryCredential{}
			}
			materials.RegistryAuth[host] = managed.Cred
			continue
		}
		row, err := e.secrets.GetByName(ctx, e.db.Runner(), projectID, registrySecretPrefix+host)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				continue
			}
			return materials, fmt.Errorf("lookup registry credential for %s: %w", host, err)
		}
		if e.cipher == nil {
			return materials, fmt.Errorf("registry credential for %s present but the master key is not available", host)
		}
		plain, err := e.cipher.Open(row.Ciphertext)
		if err != nil {
			return materials, fmt.Errorf("decrypt registry credential for %s: %w", host, err)
		}
		var cred registryCredentialJSON
		if err := json.Unmarshal(plain, &cred); err != nil {
			return materials, fmt.Errorf("registry credential secret %s: bad JSON: %w", host, err)
		}
		if materials.RegistryAuth == nil {
			materials.RegistryAuth = map[string]capability.RegistryCredential{}
		}
		materials.RegistryAuth[host] = capability.RegistryCredential{
			Server: cred.Server, Username: cred.Username, Secret: cred.Secret,
		}
	}

	// Secret 注入：process secret_refs 按名解析 → 文件材料。
	for _, name := range refs {
		row, err := e.secrets.GetByName(ctx, e.db.Runner(), projectID, name)
		if err != nil {
			return materials, fmt.Errorf("secret %q: %w", name, err)
		}
		plain, err := e.cipher.Open(row.Ciphertext)
		if err != nil {
			return materials, fmt.Errorf("decrypt secret %q: %w", name, err)
		}
		if materials.SecretFiles == nil {
			materials.SecretFiles = map[string][]byte{}
		}
		materials.SecretFiles[name] = plain
	}
	return materials, nil
}

// pinVolumes 解析卷钉住（F0.16：无显式钉住的卷在首次挂载时锚定——分配
// 策略 = 首个可用节点（N0 单控制面口径）；锚定后不可变——节点 ID 永不
// 复用）。
func (e *Engine) pinVolumes(ctx context.Context, ws []capability.Workload, projectID string) error {
	var pending []capability.VolumeMount
	for _, w := range ws {
		for _, v := range w.Volumes {
			row, err := e.volumes.GetByName(ctx, e.db.Runner(), projectID, v.VolumeID)
			if err != nil {
				return fmt.Errorf("volume %q: %w", v.VolumeID, err)
			}
			if row.PinnedNodeID == "" {
				pending = append(pending, v)
			}
		}
	}
	if len(pending) == 0 {
		return nil
	}
	nodeID, err := e.firstAvailableNode(ctx)
	if err != nil {
		return err
	}
	for _, v := range pending {
		if err := e.volumes.Pin(ctx, e.db.Runner(), projectID, v.VolumeID, nodeID); err != nil {
			return fmt.Errorf("pin volume %q: %w", v.VolumeID, err)
		}
	}
	return nil
}

// firstAvailableNode 返回首个可用节点的平台 ID（观测缓存；分配动作幂等）。
func (e *Engine) firstAvailableNode(ctx context.Context) (string, error) {
	view, err := e.runtime.DescribeCluster(ctx)
	if err != nil {
		return "", fmt.Errorf("describe cluster for volume pinning: %w", err)
	}
	for _, n := range view.Nodes {
		if n.Available && n.NodeID != "" {
			return n.NodeID, nil
		}
	}
	return "", fmt.Errorf("no available node to pin volumes to")
}

// applyVolumePinning 把卷钉住合并进 Workload Placement（卷 → 平台节点 ID
// → 调度约束；卷钉住与 Placement 绑定一律以平台节点 ID 为锚）。
//
// GetByName 错误一律硬失败（Q-8，与 pinVolumes 对偶）：漏合并钉住 = 无钉
// 住调度 = 卷可能落到别的节点（数据不可见/风险），比部署失败更糟。
func (e *Engine) applyVolumePinning(ctx context.Context, ws []capability.Workload, projectID string) error {
	for i := range ws {
		w := &ws[i] // 指针就地改（range 值副本会丢失 Placement 修改）
		for _, v := range w.Volumes {
			row, err := e.volumes.GetByName(ctx, e.db.Runner(), projectID, v.VolumeID)
			if err != nil {
				return fmt.Errorf("volume %q: %w", v.VolumeID, err)
			}
			if row.PinnedNodeID == "" {
				continue
			}
			seen := false
			for _, id := range w.Placement.NodeIDs {
				if id == row.PinnedNodeID {
					seen = true
				}
			}
			if !seen {
				w.Placement.NodeIDs = append(w.Placement.NodeIDs, row.PinnedNodeID)
			}
		}
	}
	return nil
}

// collectSecretRefs 收集 spec 的全部 secret 引用（排序稳定）。
func collectSecretRefs(spec *specv1.AppSpec) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range spec.GetProcesses() {
		for _, ref := range p.GetSecretRefs() {
			if !seen[ref] {
				seen[ref] = true
				out = append(out, ref)
			}
		}
	}
	sort.Strings(out)
	return out
}

// collectImageHosts 收集 spec 引用镜像的 registry 主机集（排序稳定）。
func collectImageHosts(spec *specv1.AppSpec) []string {
	seen := map[string]bool{}
	var out []string
	add := func(image string) {
		if host := capability.ImageRegistryHost(image); host != "" && !seen[host] {
			seen[host] = true
			out = append(out, host)
		}
	}
	if ref := spec.GetSource().GetImage().GetRef(); ref != "" {
		add(ref)
	}
	for _, p := range spec.GetProcesses() {
		if img := p.GetImage(); img != "" {
			add(img)
		}
	}
	sort.Strings(out)
	return out
}

// 镜像引用的 registry 主机提取已单源化至 capability.ImageRegistryHost
//（2026-10-03 架构评审候选 5：engine/swarm/builders 三面各持一份的收口）。

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
