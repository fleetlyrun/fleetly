package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
)

// registrySecretPrefix 是镜像凭证 Secret 的命名约定（registry:<host>；
// ADR-0014：凭证存 Secret，Ensure 解析后按节点分发）。
const registrySecretPrefix = "registry:"

// registryCredentialJSON 是 registry Secret 值的结构。
type registryCredentialJSON struct {
	Server   string `json:"server"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

// resolveMaterials 装配 Ensure 材料（ADR-0014：平台已解析后随 Ensure 下发；
// 值不落 Spec/日志——Revision 只冻结引用）。
func (e *Engine) resolveMaterials(ctx context.Context, spec *specv1.AppSpec, projectID string) (capability.Materials, error) {
	materials := capability.Materials{}
	refs := collectSecretRefs(spec)
	if len(refs) > 0 && e.cipher == nil {
		return materials, fmt.Errorf("secret refs present but the master key is not available (data root keys/ missing)")
	}

	// 镜像凭证：镜像引用的 registry host → Secret "registry:<host>"（无对应
	// Secret = 匿名拉取）。
	for _, host := range collectImageHosts(spec) {
		row, err := e.secrets.GetByName(ctx, e.db.Runner(), projectID, registrySecretPrefix+host)
		if err != nil {
			continue
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
func (e *Engine) applyVolumePinning(ctx context.Context, ws []capability.Workload, projectID string) {
	for i := range ws {
		w := &ws[i] // 指针就地改（range 值副本会丢失 Placement 修改）
		for _, v := range w.Volumes {
			row, err := e.volumes.GetByName(ctx, e.db.Runner(), projectID, v.VolumeID)
			if err != nil || row.PinnedNodeID == "" {
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
		if host := imageRegistryHostOf(image); host != "" && !seen[host] {
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

// imageRegistryHostOf 提取镜像引用的 registry 主机（与 swarm Provider 的
// host 归一同口径）。
func imageRegistryHostOf(image string) string {
	for i := 0; i < len(image); i++ {
		if image[i] != '/' {
			continue
		}
		first := image[:i]
		if containsDotOrColon(first) || first == "localhost" {
			return first
		}
		break
	}
	return "docker.io"
}

func containsDotOrColon(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' || s[i] == ':' {
			return true
		}
	}
	return false
}
