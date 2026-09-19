package substrate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/swarm"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// TestNodeToObservation 映射正确性：核心类型逐字镜像底座语义（state/
// availability 原样）、版本令牌取自底座对象版本、manager 判定来自
// ManagerStatus。
func TestNodeToObservation(t *testing.T) {
	n := swarm.Node{
		ID: "abc123",
		Meta: swarm.Meta{
			Version: swarm.Version{Index: 42},
		},
		Spec: swarm.NodeSpec{
			Annotations:  swarm.Annotations{Labels: map[string]string{"fleetly.node-id": "n_TEST"}},
			Role:         swarm.NodeRoleManager,
			Availability: swarm.NodeAvailabilityDrain,
		},
		Description: swarm.NodeDescription{Hostname: "node-1"},
		Status:      swarm.NodeStatus{State: swarm.NodeStateReady},
		ManagerStatus: &swarm.ManagerStatus{
			Leader:       true,
			Reachability: swarm.ReachabilityReachable,
		},
	}
	got := nodeToObservation(n)
	if got.SwarmNodeID != "abc123" || got.Hostname != "node-1" {
		t.Fatalf("identity fields wrong: %+v", got)
	}
	if got.State != "ready" || got.Availability != "drain" {
		t.Fatalf("state/availability must mirror substrate verbatim: %q/%q", got.State, got.Availability)
	}
	if !got.IsManager {
		t.Fatal("manager status must map to IsManager")
	}
	if got.Version.Index != 42 {
		t.Fatalf("version = %d, want 42", got.Version.Index)
	}
	if got.Labels["fleetly.node-id"] != "n_TEST" {
		t.Fatalf("labels = %+v", got.Labels)
	}

	// worker 节点：无 ManagerStatus → IsManager=false。
	n.ManagerStatus = nil
	n.Spec.Role = swarm.NodeRoleWorker
	if got := nodeToObservation(n); got.IsManager {
		t.Fatal("worker must not be manager")
	}
}

// TestToSubstrateEvent 事件映射：时间戳取底座纳秒，Type/Action 原样。
func TestToSubstrateEvent(t *testing.T) {
	at := time.Unix(1700000000, 123456789)
	got := toSubstrateEvent(events.Message{Type: events.NodeEventType, Action: "update", TimeNano: at.UnixNano()})
	if got.Type != "node" || got.Action != "update" || !got.At.Equal(at) {
		t.Fatalf("event = %+v", got)
	}
	if !state.RelevantForObservation(got) {
		t.Fatal("node events are relevant")
	}
	if state.RelevantForObservation(state.SubstrateEvent{Type: "container", Action: "start"}) {
		t.Fatal("container events are not observation-relevant")
	}
}

// TestMapSubstrateErr 错误归一：404 → ErrObjectNotFound、409 →
// ErrVersionConflict，errors.Is 链路保留原始错误。
func TestMapSubstrateErr(t *testing.T) {
	original := errdefs.ErrNotFound.WithMessage("node abc not found")
	got := mapSubstrateErr(original)
	if !errors.Is(got, state.ErrObjectNotFound) {
		t.Fatalf("not-found must map to ErrObjectNotFound, got %v", got)
	}
	if !errors.Is(got, errdefs.ErrNotFound) {
		t.Fatal("original error must stay on unwrap chain")
	}

	conflict := errdefs.ErrConflict.WithMessage("update out of sequence")
	got = mapSubstrateErr(conflict)
	if !errors.Is(got, state.ErrVersionConflict) {
		t.Fatalf("conflict must map to ErrVersionConflict, got %v", got)
	}
	if !errors.Is(got, errdefs.ErrConflict) {
		t.Fatal("original error must stay on unwrap chain")
	}

	plain := errors.New("connection refused")
	if got := mapSubstrateErr(plain); !errors.Is(got, plain) {
		t.Fatalf("plain errors must pass through, got %v", got)
	}
}

// TestClientImplementsPort 编译期端口实现校验。
var _ state.DockerClient = (*Client)(nil)

// TestSubscribeEventsStopsWithContext 事件流在 ctx 取消后关闭（无泄漏）。
func TestSubscribeEventsStopsWithContext(t *testing.T) {
	c, err := NewClient("unix:///nonexistent-socket-fleetly-test")
	if err != nil {
		t.Fatalf("construct client (lazy, no dial): %v", err)
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.SubscribeEvents(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel must close after ctx cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel not closed within 3s after ctx cancel")
	}
}

// TestNewClientHostPrecedence host 优先级行为：显式 host 必须覆盖
// DOCKER_HOST 环境变量；host 为空时回落到环境变量。观测面是 moby client
// 公开的 DaemonHost（构造是惰性的，不发起真实连接）。
func TestNewClientHostPrecedence(t *testing.T) {
	const envHost = "tcp://127.0.0.1:1"
	const explicitHost = "unix:///tmp/fleetly-test.sock"
	t.Setenv("DOCKER_HOST", envHost)

	c, err := NewClient(explicitHost)
	if err != nil {
		t.Fatalf("construct client with explicit host: %v", err)
	}
	defer func() { _ = c.Close() }()
	if got := c.cli.DaemonHost(); got != explicitHost {
		t.Fatalf("explicit host must take precedence over DOCKER_HOST: DaemonHost = %q, want %q", got, explicitHost)
	}

	cEnv, err := NewClient("")
	if err != nil {
		t.Fatalf("construct client without explicit host: %v", err)
	}
	defer func() { _ = cEnv.Close() }()
	if got := cEnv.cli.DaemonHost(); got != envHost {
		t.Fatalf("empty host must fall back to DOCKER_HOST: DaemonHost = %q, want %q", got, envHost)
	}
}
