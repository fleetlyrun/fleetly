package state

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// testLogger 是测试用静默 logger。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeDocker 是 DockerClient 端口的测试替身：可编程节点快照、版本令牌、
// label 写入与事件流，带调用计数（-race 安全）。

type fakeDocker struct {
	mu sync.Mutex

	pingErr    error
	selfNodeID string
	selfErr    error

	// nodes 是当前全量快照（SwarmNodeID → 快照）。
	nodes map[string]SubstrateNode
	// versions 是对象版本（node/service kind 前缀区分：node:<id>/service:<id>）。
	versions map[string]uint64
	// versionBump 在每次 ResolveObjectVersion 后对同 id 自增（模拟并发修改）。
	versionBump map[string]bool
	// labelUpdateConflicts 是剩余的强制冲突次数（key = node id）。
	labelUpdateConflicts map[string]int
	// labels 记录写入后的 label（node id → key → value）。
	labels map[string]map[string]string

	listCalls    int
	pingCalls    int
	updateCalls  int
	resolveCalls int
	selfCalls    int
	// resolveVersions 记录每次直读到的版本（写前直读验证用）。
	resolveVersions []uint64

	events chan SubstrateEvent
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		nodes:                map[string]SubstrateNode{},
		versions:             map[string]uint64{},
		versionBump:          map[string]bool{},
		labelUpdateConflicts: map[string]int{},
		labels:               map[string]map[string]string{},
		events:               make(chan SubstrateEvent, 8),
	}
}

func (f *fakeDocker) Ping(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pingCalls++
	return f.pingErr
}

func (f *fakeDocker) ListNodeObservations(_ context.Context) ([]SubstrateNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.pingErr != nil {
		return nil, fmt.Errorf("unreachable: %w", f.pingErr)
	}
	out := make([]SubstrateNode, 0, len(f.nodes))
	for id, n := range f.nodes {
		n.Version = ObjectVersion{Index: f.versions["node:"+id]}
		out = append(out, n)
	}
	return out, nil
}

func (f *fakeDocker) SelfNodeID(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selfCalls++
	if f.selfErr != nil {
		return "", f.selfErr
	}
	return f.selfNodeID, nil
}

func (f *fakeDocker) UpdateNodeLabel(_ context.Context, swarmNodeID, key, value string, expected ObjectVersion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// 与真实适配器同序：先检查幂等（label 已是目标值 → 无更新调用），
	// 再校验乐观令牌。
	if f.labels[swarmNodeID] != nil && f.labels[swarmNodeID][key] == value {
		return nil // 幂等 no-op，不计入更新调用
	}
	f.updateCalls++
	if f.labelUpdateConflicts[swarmNodeID] > 0 {
		f.labelUpdateConflicts[swarmNodeID]--
		return ErrVersionConflict
	}
	cur, ok := f.versions["node:"+swarmNodeID]
	if !ok {
		return ErrObjectNotFound
	}
	if cur != expected.Index {
		return ErrVersionConflict
	}
	if f.labels[swarmNodeID] == nil {
		f.labels[swarmNodeID] = map[string]string{}
	}
	f.labels[swarmNodeID][key] = value
	f.versions["node:"+swarmNodeID] = cur + 1
	return nil
}

func (f *fakeDocker) ResolveObjectVersion(_ context.Context, kind ObjectKind, id string) (ObjectVersion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolveCalls++
	key := string(kind) + ":" + id
	v, ok := f.versions[key]
	if !ok {
		return ObjectVersion{}, ErrObjectNotFound
	}
	f.resolveVersions = append(f.resolveVersions, v)
	if f.versionBump[key] {
		f.versions[key] = v + 1 // 模拟直读后被并发修改
	}
	return ObjectVersion{Index: v}, nil
}

func (f *fakeDocker) SubscribeEvents(_ context.Context) (<-chan SubstrateEvent, error) {
	return f.events, nil
}

func (f *fakeDocker) Close() error { return nil }

func (f *fakeDocker) addNode(id, hostname, nodeState string, version uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes[id] = SubstrateNode{
		SwarmNodeID: id, Hostname: hostname, State: nodeState,
		Availability: "active", Labels: map[string]string{},
	}
	f.versions["node:"+id] = version
}

// setSelf 设置 SelfNodeID 的返回（自持锁；测试不得绕过 helper 直接操作
// fake.mu——互斥锁不可重入，嵌套加锁即死锁）。
func (f *fakeDocker) setSelf(swarmNodeID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selfNodeID = swarmNodeID
	f.selfErr = err
}

// setPingErr 设置 Ping 的返回（自持锁）。
func (f *fakeDocker) setPingErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pingErr = err
}

// removeNode 移除节点快照（自持锁）。
func (f *fakeDocker) removeNode(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.nodes, id)
}

func (f *fakeDocker) snapshotCalls() (ping, list, update, resolve, self int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingCalls, f.listCalls, f.updateCalls, f.resolveCalls, f.selfCalls
}

// waitFor 轮询等待条件成立（观测循环异步，测试以终态断言）。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
