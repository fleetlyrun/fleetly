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
func (p *Provider) ensureNetworks(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload) error {
	seen := map[string]bool{}
	for _, w := range ws {
		for _, net := range w.Networks {
			if seen[net] {
				continue
			}
			seen[net] = true
			name := carrierNetworkName(ns, net)
			if _, err := p.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); err == nil {
				continue
			}
			labels := map[string]string{
				labelNetManaged:  "true",
				labelNetProject:  sanitizeNamePart(ns.Project),
				labelNetPlatform: sanitizeNamePart(net),
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
	}
	return nil
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

// ensureSecrets 把 Materials.SecretFiles 落为 swarm secret 载体（值不落
// label 或明文 env——容器内以 /run/secrets/<平台名> 文件注入，ADR-0014）。
// 返回平台名 → 载体名映射（ContainerSpec.Secrets 引用）。
func (p *Provider) ensureSecrets(ctx context.Context, m capability.Materials) (map[string]string, error) {
	out := map[string]string{}
	for name, value := range m.SecretFiles {
		carrier := secretCarrierName(name, fingerprintHex(value))
		out[name] = carrier
		if _, err := p.cli.SecretInspect(ctx, carrier, client.SecretInspectOptions{}); err == nil {
			continue
		}
		if _, err := p.cli.SecretCreate(ctx, client.SecretCreateOptions{
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
		}); err != nil {
			return nil, fmt.Errorf("swarm ensure secret %s: create: %w", carrier, err)
		}
	}
	return out, nil
}

// fingerprintHex 是材料值的短指纹（载体版本名用；非对账指纹）。
func fingerprintHex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
