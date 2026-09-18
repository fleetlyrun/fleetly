// Package substrate 是 Docker/Swarm 底座的 moby/client 适配器：把
// state.DockerClient 端口翻译为 Docker Engine API 调用。第三方类型
// （moby/swarm 结构体）只存在于本包内部，出口一律是核心类型
// （state.SubstrateNode / state.ObjectVersion / state.SubstrateEvent），
// 错误归一为端口哨兵（state.ErrObjectNotFound / ErrVersionConflict /
// ErrNotSwarmManager）——架构 §2.8 核心不出现第三方概念。
package substrate

import (
	"context"
	"fmt"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/swarm"
	mobyclient "github.com/moby/moby/client"

	"github.com/edgesets/edgefleet/internal/state"
)

// Client 是 state.DockerClient 的 moby/client 实现。
type Client struct {
	cli *mobyclient.Client
}

// NewClient 构造底座客户端。host 为空时按惯例解析：DOCKER_HOST 环境变量
// 优先，缺省本机套接字（linux unix socket / Windows named pipe）。API
// 版本协商在 moby client v0.6+ 默认开启（Engine 门禁 ≥29.8.1 由安装检查
// 负责，客户端侧不钉死）。
func NewClient(host string) (*Client, error) {
	opts := []mobyclient.Opt{mobyclient.FromEnv}
	if host != "" {
		// 显式配置优先于环境变量（WithHost 放前面会被 FromEnv 覆盖）。
		opts = []mobyclient.Opt{mobyclient.WithHost(host), mobyclient.FromEnv}
	}
	cli, err := mobyclient.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("substrate: construct docker client: %w", err)
	}
	return &Client{cli: cli}, nil
}

// Close 释放底层连接。
func (c *Client) Close() error {
	if c == nil || c.cli == nil {
		return nil
	}
	return c.cli.Close()
}

// Ping 实现端口探测。
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.cli.Ping(ctx, mobyclient.PingOptions{}); err != nil {
		return fmt.Errorf("substrate: ping: %w", err)
	}
	return nil
}

// ListNodeObservations 返回全量节点快照（逐字镜像底座语义，平台不加工）。
func (c *Client) ListNodeObservations(ctx context.Context) ([]state.SubstrateNode, error) {
	res, err := c.cli.NodeList(ctx, mobyclient.NodeListOptions{})
	if err != nil {
		return nil, fmt.Errorf("substrate: node list: %w", err)
	}
	out := make([]state.SubstrateNode, 0, len(res.Items))
	for _, n := range res.Items {
		out = append(out, nodeToObservation(n))
	}
	return out, nil
}

// nodeToObservation 把 swarm.Node 映射为核心观测类型（Labels/Version 经
// 内嵌字段 Annotations/Meta 提升后的直接选择器访问）。
func nodeToObservation(n swarm.Node) state.SubstrateNode {
	labels := make(map[string]string, len(n.Spec.Labels))
	for k, v := range n.Spec.Labels {
		labels[k] = v
	}
	return state.SubstrateNode{
		SwarmNodeID:  n.ID,
		Hostname:     n.Description.Hostname,
		State:        string(n.Status.State),
		Availability: string(n.Spec.Availability),
		IsManager:    n.ManagerStatus != nil,
		Version:      state.ObjectVersion{Index: n.Version.Index},
		Labels:       labels,
	}
}

// SelfNodeID 返回本机 Swarm node ID；未启用 Swarm 返回
// state.ErrNotSwarmManager。
func (c *Client) SelfNodeID(ctx context.Context) (string, error) {
	res, err := c.cli.Info(ctx, mobyclient.InfoOptions{})
	if err != nil {
		return "", fmt.Errorf("substrate: info: %w", err)
	}
	if res.Info.Swarm.NodeID == "" || res.Info.Swarm.LocalNodeState != swarm.LocalNodeStateActive {
		return "", state.ErrNotSwarmManager
	}
	return res.Info.Swarm.NodeID, nil
}

// UpdateNodeLabel 以乐观令牌更新节点 label：幂等（label 已是目标值时
// no-op）；令牌失效返回 state.ErrVersionConflict。
func (c *Client) UpdateNodeLabel(ctx context.Context, swarmNodeID, key, value string, expected state.ObjectVersion) error {
	res, err := c.cli.NodeInspect(ctx, swarmNodeID, mobyclient.NodeInspectOptions{})
	if err != nil {
		return mapSubstrateErr(fmt.Errorf("substrate: node inspect: %w", err))
	}
	node := res.Node
	if node.Spec.Labels[key] == value {
		return nil // 幂等 no-op（二次启动复用身份，不产生多余更新）
	}
	spec := node.Spec
	if spec.Labels == nil {
		spec.Labels = make(map[string]string, 1)
	}
	spec.Labels[key] = value
	_, err = c.cli.NodeUpdate(ctx, swarmNodeID, mobyclient.NodeUpdateOptions{
		Version: swarm.Version{Index: expected.Index},
		Spec:    spec,
	})
	if err != nil {
		return mapSubstrateErr(fmt.Errorf("substrate: node update: %w", err))
	}
	return nil
}

// ResolveObjectVersion 直读底座对象版本（node/service 两类）。
func (c *Client) ResolveObjectVersion(ctx context.Context, kind state.ObjectKind, id string) (state.ObjectVersion, error) {
	switch kind {
	case state.ObjectKindNode:
		res, err := c.cli.NodeInspect(ctx, id, mobyclient.NodeInspectOptions{})
		if err != nil {
			return state.ObjectVersion{}, mapSubstrateErr(fmt.Errorf("substrate: node inspect: %w", err))
		}
		return state.ObjectVersion{Index: res.Node.Version.Index}, nil
	case state.ObjectKindService:
		res, err := c.cli.ServiceInspect(ctx, id, mobyclient.ServiceInspectOptions{})
		if err != nil {
			return state.ObjectVersion{}, mapSubstrateErr(fmt.Errorf("substrate: service inspect: %w", err))
		}
		return state.ObjectVersion{Index: res.Service.Version.Index}, nil
	default:
		return state.ObjectVersion{}, fmt.Errorf("substrate: unknown object kind %q", kind)
	}
}

// SubscribeEvents 订阅底座事件流：仅转发 node/service/task 类事件（观测
// 缓存失效信号），channel 在 ctx 取消或流结束时关闭。
func (c *Client) SubscribeEvents(ctx context.Context) (<-chan state.SubstrateEvent, error) {
	res := c.cli.Events(ctx, mobyclient.EventsListOptions{})
	out := make(chan state.SubstrateEvent)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-res.Err:
				// 流错误/结束（含 ctx 取消）都终结本次订阅：关闭 channel，
				// 消费方（observer eventsLoop）负责退避重连。
				_ = err
				return
			case msg, ok := <-res.Messages:
				if !ok {
					return
				}
				out <- toSubstrateEvent(msg)
			}
		}
	}()
	return out, nil
}

// toSubstrateEvent 把底座事件映射为核心事件类型。
func toSubstrateEvent(msg events.Message) state.SubstrateEvent {
	at := time.Now().UTC()
	if msg.TimeNano > 0 {
		at = time.Unix(0, msg.TimeNano).UTC()
	} else if msg.Time > 0 {
		at = time.Unix(msg.Time, 0).UTC()
	}
	return state.SubstrateEvent{Type: string(msg.Type), Action: string(msg.Action), At: at}
}

// mapSubstrateErr 把底座传输错误归一为端口哨兵（errors.Is 链路保留）。
func mapSubstrateErr(err error) error {
	switch {
	case errdefs.IsNotFound(err):
		return fmt.Errorf("%w: %w", state.ErrObjectNotFound, err)
	case errdefs.IsConflict(err):
		return fmt.Errorf("%w: %w", state.ErrVersionConflict, err)
	}
	return err
}
