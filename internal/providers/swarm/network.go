package swarm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// 网络载体命名与标记（Provider 私有）。
const (
	labelNetManaged  = "fleetly.net.managed"
	labelNetProject  = "fleetly.ns.project"
	labelNetPlatform = "fleetly.net.name" // 平台网络名（spec networks 引用锚）
	networkPrefix    = "fleetly-net"
)

// carrierNetworkName 计算网络载体名：fleetly-net-<project>-<name>。
func carrierNetworkName(ns capability.NamespaceRef, platformName string) string {
	return sanitizeNamePart(networkPrefix + "-" + ns.Project + "-" + platformName)
}

// ensureNetworks create-or-get Workload 引用的全部平台网络（per-Project
// overlay；egress:none 网络 = 独立 overlay + 不发布端口 + 不注入跨网 DNS
// ——swarm v1 弱隔离，出网不阻断，能力边界经 Describe Notes 明示）。
// 同域引用（w.Networks）与跨域引用（w.NetworkRefs——受管 Edge 挂项目网）
// 都在此落载体；引用网络可能尚无任何用户 Workload 挂靠，首次由此创建。
func (p *Provider) ensureNetworks(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload) error {
	type netRef struct {
		ns   capability.NamespaceRef
		name string
	}
	refs := map[netRef]bool{}
	for _, w := range ws {
		for _, net := range w.Networks {
			refs[netRef{ns: ns, name: net}] = true
		}
		for _, ref := range w.NetworkRefs {
			refs[netRef{ns: ref.Namespace, name: ref.Name}] = true
		}
	}
	for ref := range refs {
		name := carrierNetworkName(ref.ns, ref.name)
		if err := p.inspectNetworkCarrier(ctx, name); err == nil {
			continue // create-or-get 的 get 半边：已存在即复用
		} else if !isNotFound(err) {
			// Q-20：inspect 失败 ≠ 不存在——权限/连接类错误必须上抛带
			// 原因，不得伪装 404 触发 create（撞既有载体名只会得到误导性
			// 的"already exists"）。对照 service 路径 isNotFound 先例。
			return fmt.Errorf("swarm ensure network %s: inspect: %w", name, err)
		}
		labels := map[string]string{
			labelNetManaged:  "true",
			labelNetProject:  sanitizeNamePart(ref.ns.Project),
			labelNetPlatform: sanitizeNamePart(ref.name),
		}
		if _, err := p.cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
			Driver: "overlay",
			Labels: labels,
			// swarm v1 弱隔离口径（领域模型 §6）：不设 Internal——
			// 出网不阻断、明示弱隔离的诚实边界（架构 §10）。
		}); err != nil {
			return fmt.Errorf("swarm ensure network %s: create: %w", name, err)
		}
	}
	return nil
}

// inspectNetworkCarrier 查网络载体存在性（测试缝优先；存在返回 nil，
// 不存在返回 NotFound，其余错误原样带出）。
func (p *Provider) inspectNetworkCarrier(ctx context.Context, name string) error {
	if p.networkInspect != nil {
		return p.networkInspect(ctx, name)
	}
	_, err := p.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	return err
}

// secretCarrierName 是 Secret 载体名（版本化：fleetly-sec-<name>-<fp8>；
// swarm secret 不可变——值变更走新版本名，旧版本残留按孤儿策略只登记）。
func secretCarrierName(platformName, fingerprintHex string) string {
	fp := fingerprintHex
	if len(fp) > 8 {
		fp = fp[:8]
	}
	return sanitizeNamePart("fleetly-sec-" + platformName + "-" + fp)
}

// secretCarrier 是 Secret 载体的引用面（id + 名双发——swarmkit
// validateSecretRefsSpec 要求 SecretID 与 SecretName 同时在场：docker CLI
// 是客户端解析名→ID 后下发，raw API 只发名会被 "malformed secret
// reference" 拒绝；2026-10-02 staging 真机首炸，F1.12 只有 FakeRuntime
// 覆盖）。
type secretCarrier struct {
	id   string
	name string
}

// ensureSecrets 把 Materials.SecretFiles 落为 swarm secret 载体（值不落
// label 或明文 env——容器内以 /run/secrets/<平台名> 文件注入，ADR-0014）。
// 返回平台名 → 载体引用映射（ContainerSpec.Secrets 引用）。
func (p *Provider) ensureSecrets(ctx context.Context, m capability.Materials) (map[string]secretCarrier, error) {
	out := map[string]secretCarrier{}
	for name, value := range m.SecretFiles {
		carrier := secretCarrierName(name, fingerprintHex(value))
		if res, err := p.inspectSecretCarrier(ctx, carrier); err == nil {
			// create-or-get 的 get 半边：已存在即复用（ID 随行）。
			out[name] = secretCarrier{id: res.Secret.ID, name: carrier}
			continue
		} else if !isNotFound(err) {
			// Q-20：同 ensureNetworks——非 NotFound 错误上抛带原因，不
			// 伪装 404 触发 create。
			return nil, fmt.Errorf("swarm ensure secret %s: inspect: %w", carrier, err)
		}
		resp, err := p.cli.SecretCreate(ctx, client.SecretCreateOptions{
			Spec: swarm.SecretSpec{
				Annotations: swarm.Annotations{
					Name: carrier,
					Labels: map[string]string{
						labelManaged: "true",
						labelProcess: sanitizeNamePart(name),
					},
				},
				Data: value,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("swarm ensure secret %s: create: %w", carrier, err)
		}
		out[name] = secretCarrier{id: resp.ID, name: carrier}
	}
	return out, nil
}

// inspectSecretCarrier 查 Secret 载体（测试缝优先；语义同
// inspectNetworkCarrier——返回载体体以随行引用 ID）。
func (p *Provider) inspectSecretCarrier(ctx context.Context, name string) (client.SecretInspectResult, error) {
	if p.secretInspect != nil {
		return p.secretInspect(ctx, name)
	}
	return p.cli.SecretInspect(ctx, name, client.SecretInspectOptions{})
}

// fingerprintHex 是材料值的短指纹（载体版本名用；非对账指纹）。
func fingerprintHex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
