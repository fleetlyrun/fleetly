package k3s

// 内容寻址规划与 controller-wins 判定（自 runtime.go 拆出，2026-11-24——
// sizeguard 单文件 1200 行预算；仅搬迁，零行为变更）。

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// planContentAddressed 是 k3s 的内容寻址三分决策（ADR-0064 决策 1/2，免
// 服务端内容比对——k8s 服务端对入站写自带 defaulting 归一，客户端深比
// 对会因 defaulted 字段恒不等）：
//   - A == 账本 A 键（modulo-gen 全等价）→ skip：零写，wire-gen = issuedGen
//     旧值（观测归因锚不漂）。
//   - B == 账本 B 键（modulo-gen 且 modulo-replicas 等价）→ replicas-only：
//     写代钉在 issuedGen——模板字节不动（服务端只 scale），wire-gen 保持。
//   - A 键缺席（冷账本——daemon 重启重放形态）→ 读现存对象 meta 代戳钉
//     写代：内容未变则服务端归一后 no-op（重启零滚动）；已变则该载体滚
//     动但代戳停旧值——归因自洽（pod 标签=报告=账本三方一致），L1 按报
//     告圈集，边界记 ADR。coldLedger=true 报告"本次写是重启重放形态"，
//     调用方不得计入 Changed（ADR-0082 §2 第 3 条）。
//   - 其余 = 内容变更 → 写代 = 调用 gen（滚动替换，代戳随内容推进）。
//
// controller-wins 前置（2026-11-24）：A 键命中只在「服务端对象仍是那次下发
// 的样子」时才成立——对象已不在（live 无此键）或 metadata.generation 自
// 写入确认后推进过（平台外 scale / edit），账本作废、放行到写路径：重建取
// create 形态（写代 = 调用 gen），修复取 issuedGen 钉写代（模板字节不动，
// 只把副本数等 spec 压回）。live 是本拍 Ensure 的现存对象索引（零额外请求）。
func (p *Provider) planContentAddressed(ctx context.Context, nsName, kind, name, a, b string, w capability.Workload, gen capability.Generation, live map[string]domainObject) (writeGen uint64, skip, coldLedger bool) {
	key := kind + "/" + name
	lastA := p.lastIssuedOf(key)
	callGen := uint64(gen)
	if lastA != "" && a == lastA {
		liveObj, liveExists := live[key]
		if !liveExists || p.objectTouchedSinceIssued(key, liveObj.generation) {
			p.forgetLastIssued(key) // 作废 A/B/版本三键：putObject 的账本闸随之放行
			if liveExists {
				if g, ok := p.issuedGenOf(w.ID); ok {
					return g, false, false // 平台外改动：写代钉在 issuedGen
				}
			}
			return callGen, false, false // 重建（或 issuedGen 缺席）：写代 = 调用 gen
		}
		if g, ok := p.issuedGenOf(w.ID); ok {
			return g, true, false
		}
		return callGen, true, false // 账本在而 issuedGen 缺席的异常形态：保守跳过
	}
	if lastA != "" && b == p.lastIssuedBOf(key) {
		if g, ok := p.issuedGenOf(w.ID); ok {
			return g, false, false // replicas-only：模板钉 wire-gen，只动副本数
		}
		return callGen, false, false
	}
	if lastA == "" {
		if g := p.serverObjectGen(ctx, nsName, kind, name); g > 0 {
			return g, false, true // 冷账本重放：钉现存代戳（内容未变 → no-op 零滚）
		}
	}
	return callGen, false, false
}

// serverObjectGen 读现存对象的 meta 代戳（冷账本重放的写代钉住锚；404
// 或读失败返回 0 = create 形态）。
func (p *Provider) serverObjectGen(ctx context.Context, nsName, kind, name string) uint64 {
	switch kind {
	case "deployment":
		if d, err := p.cli.AppsV1().Deployments(nsName).Get(ctx, name, metav1.GetOptions{}); err == nil {
			g, _ := parseUint(d.Labels[labelGeneration])
			return g
		}
	case "daemonset":
		if ds, err := p.cli.AppsV1().DaemonSets(nsName).Get(ctx, name, metav1.GetOptions{}); err == nil {
			g, _ := parseUint(ds.Labels[labelGeneration])
			return g
		}
	case "pod":
		if pod, err := p.cli.CoreV1().Pods(nsName).Get(ctx, name, metav1.GetOptions{}); err == nil {
			g, _ := parseUint(pod.Labels[labelGeneration])
			return g
		}
	}
	return 0
}

// serverObjectGeneration 读现存服务端对象的 metadata.generation（404、
// 读失败、或该 kind 无版本语义 → 0）。
func (p *Provider) serverObjectGeneration(ctx context.Context, nsName, kind, name string) int64 {
	switch kind {
	case "deployment":
		if d, err := p.cli.AppsV1().Deployments(nsName).Get(ctx, name, metav1.GetOptions{}); err == nil {
			return d.Generation
		}
	case "daemonset":
		if ds, err := p.cli.AppsV1().DaemonSets(nsName).Get(ctx, name, metav1.GetOptions{}); err == nil {
			return ds.Generation
		}
	}
	return 0
}

// confirmIssuedObjectGen 是写入成功后的版本确认：回读服务端对象的
// metadata.generation 入账。读失败（瞬断）或该 kind 无版本语义则保持未知
// （0）——未知不是证据，objectTouchedSinceIssued 不据此重写。
func (p *Provider) confirmIssuedObjectGen(ctx context.Context, nsName, key, kind, name string) {
	if g := p.serverObjectGeneration(ctx, nsName, kind, name); g > 0 {
		p.recordIssuedObjectGen(key, g)
	}
}

// objectTouchedSinceIssued 报告服务端对象的 generation 是否自本 Provider
// 写入确认后推进过（= 平台外改过 spec）。任一侧未知（0）一律判未动——
// 不凭空重写。
func (p *Provider) objectTouchedSinceIssued(key string, liveGeneration int64) bool {
	recorded := p.issuedObjectGenOf(key)
	return recorded != 0 && liveGeneration != 0 && recorded != liveGeneration
}
