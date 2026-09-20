package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"
)

// 锚定 duty（multi-node §2.7/D-MN-8）：worker 平台身份由 manager 收编——
// 观测拍（observer resync / 事件驱动失效）后对全量快照逐节点：
//
//  1. 无 fleetly.node-id label 的节点 → 铸造 n_<ULID> → 写 label（写前
//     直读版本令牌 + 冲突重试，沿用 NodeIdentity 既有纪律）→ 登记
//     runtime_node_refs → 审计（identity_created/anchored）。
//  2. label 已存在 → 以 label 值登记 ref（labels 是持久载体（raft 复制），
//     SQLite ref 可整表重建——L1/L2 恢复后由本 duty 从 label 反建）。
//  3. 冲突（swarm 节点已映射到另一平台 ID / label 值撞已有映射 / label
//     缺失但映射尚在）→ 不自动消解：审计 + 管理面警示日志，人工走
//     rebind 路径（D-PLC-6 人工 rebind 的集群版）。
//
// 本机节点沿用既有锚定（NodeIdentity：meta → label → ref），本 duty 跳过。
//
// node.* 产品事件（§5.3 只增清单）随同一拍差分发出：node.joined（快照
// 新现）、node.down/node.up（state ready↔down 转移，Swarm 失联判定语义）、
// node.removed（快照消失）、node.availability_changed（active↔drain/pause，
// 载荷 old/new）。逐转移发事件、不做防抖（诚实：每拍转移都可见）——v0.2
// 起解除「observer 不产生产品事件」的 v0.1 注记。

// ClusterAnchor 是集群锚定与节点事件 duty。
type ClusterAnchor struct {
	store  *Store
	docker DockerClient
	log    *slog.Logger
}

// NewClusterAnchor 构造锚定 duty。
func NewClusterAnchor(store *Store, d DockerClient, log *slog.Logger) *ClusterAnchor {
	return &ClusterAnchor{store: store, docker: d, log: log}
}

// PostSync 是 Observer 的观测拍后处理入口（WithPostSync 注入）：锚定收编
// + 快照差分事件。失败只日志告警、不推翻观测同步——锚定与事件均为幂等
// 收敛语义，下一拍自动重试。
func (a *ClusterAnchor) PostSync(ctx context.Context, prev []CachedNode, next []SubstrateNode) {
	if _, err := a.Reconcile(ctx, prev, next); err != nil {
		a.log.Warn("cluster anchor reconcile failed (retries on next observation beat)", "error", err)
	}
}

// ReconcileResult 是一拍收编与差分的账目（测试与运维面）。
type ReconcileResult struct {
	// Minted 是本次铸造平台 ID 的节点（swarm node ID）。
	Minted []string
	// Rebuilt 是本次从 label 反建 ref 的平台 ID（labels 载体 → SQLite ref）。
	Rebuilt []string
	// Conflicts 是披露未消解的冲突（不自动消解，人工 rebind）。
	Conflicts []string
	// Joined/Removed/Up/Down/AvailabilityChanged 是差分事件计数。
	Joined, Removed, Up, Down, AvailabilityChanged int
}

// Reconcile 执行一拍：非本机节点逐个收编，随后 prev→next 差分发 node.*
// 事件。prev 为空（首次观测）时差分只报 joined。
func (a *ClusterAnchor) Reconcile(ctx context.Context, prev []CachedNode, next []SubstrateNode) (ReconcileResult, error) {
	var res ReconcileResult

	// 本机节点沿用既有锚定（NodeIdentity duty 负责 meta → label → ref）。
	selfID, err := a.docker.SelfNodeID(ctx)
	if err != nil {
		if errors.Is(err, ErrNotSwarmManager) {
			// Swarm 未启用：无集群可收编（观测面同样拿不到快照，本拍空转）。
			return res, nil
		}
		return res, fmt.Errorf("state: cluster anchor self node id: %w", err)
	}

	for i := range next {
		n := next[i]
		if n.SwarmNodeID == selfID {
			continue // 本机节点：既有锚定路径，不收编
		}
		if err := a.anchorNode(ctx, n, &res); err != nil {
			return res, err
		}
	}

	a.diffEvents(ctx, prev, next, &res)
	return res, nil
}

// anchorNode 收编单节点（铸造 / 反建 / 冲突披露三态）。
func (a *ClusterAnchor) anchorNode(ctx context.Context, n SubstrateNode, res *ReconcileResult) error {
	label := n.Labels[LabelNodeID]
	if label != "" {
		// label 已存在：先查该平台 ID 的既有映射——label 值撞已有映射
		//（同平台 ID 已锚在另一 swarm 节点）是冲突，不是换机重锚：收编
		// duty 无权改写映射（NodeIdentity 的重锚语义只属本机节点）。
		existing, err := a.store.GetRuntimeNodeRef(ctx, label)
		switch {
		case err == nil && existing.SwarmNodeID != n.SwarmNodeID:
			a.discloseConflict(ctx, n, label,
				"node label value is already mapped to another swarm node (existing mapping kept; no auto-resolution)")
			res.Conflicts = append(res.Conflicts, n.SwarmNodeID)
			return nil
		case err == nil:
			return nil // label 与映射一致：稳态，无动作
		case !errors.Is(err, ErrRefNotFound):
			return fmt.Errorf("state: read node ref %s: %w", label, err)
		}
		// 反建路径：labels 是持久载体，SQLite ref 可整表重建（§2.7 步骤 2）。
		changed, err := a.store.UpsertRuntimeNodeRef(ctx, label, n.SwarmNodeID, time.Now().UTC())
		if err != nil {
			if errors.Is(err, ErrVersionConflict) {
				a.discloseConflict(ctx, n, label, "node label value collides with an existing platform id mapping")
				res.Conflicts = append(res.Conflicts, n.SwarmNodeID)
				return nil
			}
			return fmt.Errorf("state: anchor node %s: %w", n.SwarmNodeID, err)
		}
		if changed {
			if err := a.store.InTx(ctx, func(tx *Tx) error {
				return tx.WriteAudit(ctx, AuditEntry{
					Actor:       "system",
					Action:      "node.identity_anchored",
					Target:      "node:" + label,
					Result:      "ok",
					DiffSummary: DiffSummary("swarm_node_id", n.SwarmNodeID, "rebuilt_from_label", "true"), // MG-6 构造器
				})
			}); err != nil {
				return fmt.Errorf("state: audit node ref rebuild: %w", err)
			}
			a.log.Info("runtime node ref rebuilt from swarm label", "node_id", label, "swarm_node_id", n.SwarmNodeID)
			res.Rebuilt = append(res.Rebuilt, label)
		}
		return nil
	}

	// label 缺失：先查映射占用——已有映射而 label 缺失 = label 被外部删改，
	// 重铸会静默换锚破坏绑定，不猜测、不消解（§2.7 步骤 3 冲突族）。
	if _, err := a.store.GetRuntimeNodeRefBySwarmID(ctx, n.SwarmNodeID); err == nil {
		a.discloseConflict(ctx, n, "", "swarm node has a platform id mapping but its fleetly.node-id label is missing (removed externally?)")
		res.Conflicts = append(res.Conflicts, n.SwarmNodeID)
		return nil
	} else if !errors.Is(err, ErrRefNotFound) {
		return fmt.Errorf("state: read node ref by swarm id %s: %w", n.SwarmNodeID, err)
	}

	// 铸造路径（§2.7 步骤 1）：n_<ULID> → label（写前直读取令牌 + 冲突
	// 重试，NodeIdentity 同纪律）→ ref → 审计。
	id := NodeIDPrefix + ulid.Make().String()
	if err := updateNodeLabelWithRetry(ctx, a.docker, a.log, n.SwarmNodeID, LabelNodeID, id); err != nil {
		return fmt.Errorf("state: mint node id label for %s: %w", n.SwarmNodeID, err)
	}
	if _, err := a.store.UpsertRuntimeNodeRef(ctx, id, n.SwarmNodeID, time.Now().UTC()); err != nil {
		if errors.Is(err, ErrVersionConflict) {
			a.discloseConflict(ctx, n, id, "minted platform id collides with an existing mapping for the swarm node")
			res.Conflicts = append(res.Conflicts, n.SwarmNodeID)
			return nil
		}
		return fmt.Errorf("state: record minted node ref %s: %w", id, err)
	}
	if err := a.store.InTx(ctx, func(tx *Tx) error {
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:  "system",
			Action: "node.identity_created",
			Target: "node:" + id,
			Result: "ok",
		}); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:       "system",
			Action:      "node.identity_anchored",
			Target:      "node:" + id,
			Result:      "ok",
			DiffSummary: DiffSummary("swarm_node_id", n.SwarmNodeID, "hostname", n.Hostname), // MG-6 构造器
		})
	}); err != nil {
		return fmt.Errorf("state: audit node identity mint: %w", err)
	}
	a.log.Info("platform node id minted for joined swarm node", "node_id", id, "swarm_node_id", n.SwarmNodeID, "hostname", n.Hostname)
	res.Minted = append(res.Minted, n.SwarmNodeID)
	return nil
}

// discloseConflict 落审计 + 警示日志（冲突不自动消解：管理面披露，人工走
// rebind；§5.3 事件清单无冲突事件名——披露载体是审计记录与管理面日志）。
func (a *ClusterAnchor) discloseConflict(ctx context.Context, n SubstrateNode, label, reason string) {
	if err := a.store.InTx(ctx, func(tx *Tx) error {
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:       "system",
			Action:      "node.identity_conflict",
			Target:      "node:" + n.SwarmNodeID,
			Result:      "error",
			ErrorCode:   "",
			DiffSummary: DiffSummary("reason", reason, "label", label, "hostname", n.Hostname), // MG-6 构造器
		})
	}); err != nil {
		a.log.Error("write node identity conflict audit failed", "swarm_node_id", n.SwarmNodeID, "error", err)
	}
	a.log.Warn("node identity conflict detected — NOT auto-resolved, manual rebind required (multi-node §2.7)",
		"swarm_node_id", n.SwarmNodeID, "hostname", n.Hostname, "label", label, "reason", reason)
}

// diffEvents 对 prev→next 快照差分发 node.* 事件（逐转移、不做防抖）。
func (a *ClusterAnchor) diffEvents(ctx context.Context, prev []CachedNode, next []SubstrateNode, res *ReconcileResult) {
	prevBySwarm := make(map[string]CachedNode, len(prev))
	for _, p := range prev {
		prevBySwarm[p.SwarmNodeID] = p
	}
	nextBySwarm := make(map[string]bool, len(next))
	for i := range next {
		n := next[i]
		nextBySwarm[n.SwarmNodeID] = true
		p, seen := prevBySwarm[n.SwarmNodeID]
		platformID := n.Labels[LabelNodeID]
		subject := "node:" + platformID
		if subject == "node:" {
			subject = "node:" + n.SwarmNodeID
		}
		if !seen {
			// node.joined（新 swarm 节点；锚定是否完成如实进载荷）。
			if a.emit(ctx, "node.joined", subject, map[string]string{
				"swarm_node_id": n.SwarmNodeID, "hostname": n.Hostname,
				"platform_id": platformID, "anchored": boolStr(platformID != ""),
			}) {
				res.Joined++
			}
			continue
		}
		// state ready↔down 转移（Swarm 失联判定语义，文案纪律沿用）。
		if p.State != n.State {
			switch {
			case n.State == "ready":
				if a.emit(ctx, "node.up", subject, map[string]string{
					"swarm_node_id": n.SwarmNodeID, "hostname": n.Hostname,
					"old": p.State, "new": n.State,
				}) {
					res.Up++
				}
			case p.State == "ready":
				if a.emit(ctx, "node.down", subject, map[string]string{
					"swarm_node_id": n.SwarmNodeID, "hostname": n.Hostname,
					"old": p.State, "new": n.State,
				}) {
					res.Down++
				}
				// 非 ready → 非 ready 的中间态迁移（unknown/disconnected 间）
				// 不占用 node.up/down 词面：失联叙事已由首拍 down 承载。
			}
		}
		// availability 转移（active↔drain/pause；drain 维护窗口叙事载体）。
		if p.Availability != n.Availability {
			if a.emit(ctx, "node.availability_changed", subject, map[string]string{
				"swarm_node_id": n.SwarmNodeID, "hostname": n.Hostname,
				"old": p.Availability, "new": n.Availability,
			}) {
				res.AvailabilityChanged++
			}
		}
	}
	for _, p := range prev {
		if !nextBySwarm[p.SwarmNodeID] {
			if a.emit(ctx, "node.removed", "node:"+p.SwarmNodeID, map[string]string{
				"swarm_node_id": p.SwarmNodeID, "hostname": p.Hostname,
			}) {
				res.Removed++
			}
		}
	}
}

// emit 落一条产品事件（Outbox 事务；失败只日志——事件披露不阻断观测拍）。
func (a *ClusterAnchor) emit(ctx context.Context, name, subject string, payload map[string]string) bool {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte("{}")
	}
	err = a.store.InTx(ctx, func(tx *Tx) error {
		_, err := tx.AppendEvent(ctx, Event{Name: name, Subject: subject, Payload: string(raw)})
		return err
	})
	if err != nil {
		a.log.Warn("node event append failed", "event", name, "subject", subject, "error", err)
		return false
	}
	return true
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// updateNodeLabelWithRetry 以写前直读纪律写节点 label：每次重试先重取底座
// 对象版本作乐观令牌；底座并发冲突（ErrVersionConflict）则重取重试
// （NodeIdentity 与 ClusterAnchor 共用——锚定 label 写入的唯一路径）。
func updateNodeLabelWithRetry(ctx context.Context, d DockerClient, log *slog.Logger, swarmNodeID, key, value string) error {
	var lastErr error
	for i := 0; i < labelWriteRetries; i++ {
		// 写前直读：直读底座节点版本作乐观令牌（state-model §2.2 读契约）。
		version, err := d.ResolveObjectVersion(ctx, ObjectKindNode, swarmNodeID)
		if err != nil {
			return fmt.Errorf("state: resolve node version: %w", err)
		}
		if err := d.UpdateNodeLabel(ctx, swarmNodeID, key, value, version); err == nil {
			return nil
		} else if !errors.Is(err, ErrVersionConflict) {
			return fmt.Errorf("state: update node label %s: %w", key, err)
		} else {
			lastErr = err
		}
		log.Warn("node label write hit concurrent modification, retrying with fresh version token",
			"swarm_node_id", swarmNodeID, "attempt", i+1)
	}
	return fmt.Errorf("state: update node label %s after %d attempts: %w", key, labelWriteRetries, lastErr)
}
