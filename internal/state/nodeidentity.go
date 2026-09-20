package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// 节点身份（state-model §2.3）：领域身份 = 平台节点 ID（n_<ULID>，用户
// 裁决 D-STM-8），首次启动生成并持久于 meta（二次启动复用同一 ID），
// 随后锚定到 Swarm node label fleetly.node-id；Swarm node ID 仅存
// runtime_node_refs 适配器映射。
//
// 锚定走写前直读纪律：ResolveObjectVersion 取节点版本令牌 → 以该令牌
// 更新 label → 底座报并发冲突（ErrVersionConflict）则重取令牌重试，
// 指数退避循环直到成功或停机（底座暂时不可达不崩溃、不放弃）。

// NodeIDPrefix 是平台节点 ID 前缀。
const NodeIDPrefix = "n_"

// 锚定重试参数：初值 1s、指数退避、上限 60s；label 写入的乐观令牌冲突
// 重试上限（每次重试都会先重取底座版本）。
const (
	anchorBackoffInitial = 1 * time.Second
	anchorBackoffMax     = 60 * time.Second
	labelWriteRetries    = 3
)

// NodeIdentity 管理本机平台节点身份的生成、持久化与底座锚定。
type NodeIdentity struct {
	store  *Store
	docker DockerClient
	log    *slog.Logger

	mu         sync.Mutex
	platformID string
	anchored   bool

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// NewNodeIdentity 构造节点身份管理器。
func NewNodeIdentity(store *Store, d DockerClient, log *slog.Logger) *NodeIdentity {
	done := make(chan struct{})
	close(done) // 未 Start 前 Stop 立即返回（lynx 生命周期契约：Stop 须容忍未 Start）
	return &NodeIdentity{
		store:  store,
		docker: d,
		log:    log,
		stop:   make(chan struct{}),
		done:   done,
	}
}

// PlatformID 返回已确保的平台节点 ID（EnsurePlatformID 成功前为空串）。
func (n *NodeIdentity) PlatformID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.platformID
}

// Anchored 报告身份是否已锚定到 Swarm 节点 label 并登记映射。
func (n *NodeIdentity) Anchored() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.anchored
}

// EnsurePlatformID 确保 meta 中存在本机平台节点 ID：缺失则生成 n_<ULID>
// 并与系统审计同事务落库（系统自动动作必入审计，state-model §2.9）。
// 幂等；返回平台节点 ID。
func (n *NodeIdentity) EnsurePlatformID(ctx context.Context) (string, error) {
	existing, err := n.store.GetMeta(ctx, MetaKeyPlatformNodeID)
	if err != nil {
		return "", fmt.Errorf("state: read platform node id: %w", err)
	}
	if existing != "" {
		n.mu.Lock()
		n.platformID = existing
		n.mu.Unlock()
		return existing, nil
	}
	id := NodeIDPrefix + ulid.Make().String()
	err = n.store.InTx(ctx, func(tx *Tx) error {
		if err := tx.SetMeta(ctx, MetaKeyPlatformNodeID, id); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:  "system",
			Action: "node.identity_created",
			Target: "node:" + id,
			Result: "ok",
		})
	})
	if err != nil {
		return "", fmt.Errorf("state: persist platform node id: %w", err)
	}
	n.mu.Lock()
	n.platformID = id
	n.mu.Unlock()
	n.log.Info("platform node id created", "node_id", id)
	return id, nil
}

// Anchor 执行一次完整锚定：自省本机 Swarm node ID → 幂等写身份 label
// （写前直读取令牌 + 冲突重试）→ 登记 runtime_node_refs 映射（变更时
// 写系统审计）。引擎未启用 Swarm 返回 ErrNotSwarmManager（调用方退避
// 重试，例如安装流程 swarm init 完成前）。
func (n *NodeIdentity) Anchor(ctx context.Context) error {
	if n.platformID == "" {
		return errors.New("state: platform node id not ensured")
	}
	swarmNodeID, err := n.docker.SelfNodeID(ctx)
	if err != nil {
		return fmt.Errorf("state: self node id: %w", err)
	}
	anchoredAt := time.Now().UTC()
	if err := n.updateNodeLabelWithRetry(ctx, swarmNodeID, LabelNodeID, n.platformID); err != nil {
		return err
	}
	changed, err := n.store.UpsertRuntimeNodeRef(ctx, n.platformID, swarmNodeID, anchoredAt)
	if err != nil {
		return fmt.Errorf("state: record runtime node ref: %w", err)
	}
	if changed {
		err = n.store.InTx(ctx, func(tx *Tx) error {
			return tx.WriteAudit(ctx, AuditEntry{
				Actor:       "system",
				Action:      "node.identity_anchored",
				Target:      "node:" + n.platformID,
				Result:      "ok",
				DiffSummary: DiffSummary("swarm_node_id", swarmNodeID), // MG-6：构造器替换手拼 JSON
			})
		})
		if err != nil {
			return fmt.Errorf("state: audit identity anchor: %w", err)
		}
		n.log.Info("platform node id anchored to swarm node", "node_id", n.platformID, "swarm_node_id", swarmNodeID)
	}
	n.mu.Lock()
	n.anchored = true
	n.mu.Unlock()
	return nil
}

// updateNodeLabelWithRetry 以写前直读纪律写节点身份 label：每次重试先
// 重取底座对象版本作乐观令牌；底座并发冲突（ErrVersionConflict）或
// 幂等重放安全——label 目标值即平台 ID，重复写无副作用。
// 实现与 ClusterAnchor 共用包级 helper（锚定 label 写入的唯一路径）。
func (n *NodeIdentity) updateNodeLabelWithRetry(ctx context.Context, swarmNodeID, key, value string) error {
	return updateNodeLabelWithRetry(ctx, n.docker, n.log, swarmNodeID, key, value)
}

// Start 启动锚定守护：EnsurePlatformID 已在装配期完成（Init 阶段
// fail-fast），此处循环执行 Anchor 直到成功或 ctx 取消；失败按指数退避
// 重试（底座未就绪/重启窗口不崩溃、不放弃）。非阻塞。
// done 通道在 Start 内重开（lynx 生命周期保证 Start 先于 Stop 顺序执行，
// 无并发写）。
func (n *NodeIdentity) Start(ctx context.Context) error {
	n.done = make(chan struct{})
	go n.loop(ctx)
	return nil
}

// Stop 通知守护退出并等待其结束。
func (n *NodeIdentity) Stop(_ context.Context) error {
	n.stopOnce.Do(func() {
		if n.stop != nil {
			close(n.stop)
		}
	})
	if n.done != nil {
		<-n.done
	}
	return nil
}

// CheckHealth 报告身份就绪态：平台 ID 已生成即视为身份层健康（底座锚定
// 的健康语义由 observer 的底座检查承载，不在此重复——避免 swarm 未启用
// 的单机上身份检查拖垮 readiness）。
func (n *NodeIdentity) CheckHealth() error {
	if n.platformID == "" {
		return errors.New("state: platform node identity not ensured")
	}
	return nil
}

func (n *NodeIdentity) loop(ctx context.Context) {
	defer close(n.done)
	backoff := anchorBackoffInitial
	for {
		err := n.Anchor(ctx)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if !errors.Is(err, ErrNotSwarmManager) {
			// 非「未启用 Swarm」类失败同样退避重试（底座抖动/冲突耗尽）。
			n.log.Warn("node identity anchor failed, retrying", "error", err, "backoff", backoff.String())
		} else {
			n.log.Info("swarm mode not active yet, node identity anchor deferred", "backoff", backoff.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-n.stop:
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > anchorBackoffMax {
			backoff = anchorBackoffMax
		}
	}
}
