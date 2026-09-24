package placement

// 多节点解析与选点测试（multi-node §2.6/D-MN-7，E1-7）：候选集 = 底座
// 直读全量快照；label 值域 = 唯一显示名或平台 ID（歧义 → INVALID + 提示
// 平台 ID）；三因子 = 数据引力 > 已钉数 > 平台 ID 字典序；候选集唯一 =
// 本机时与 v0.1 自动绑定行为等价。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
	testsupport "github.com/fleetlyrun/fleetly/internal/testsupport"
)

// addWorker 向 harness 快照追加一个已锚定且 ready 的 worker。
func (h *harness) addWorker(hostname, platformID string) {
	h.docker.nodes = append(h.docker.nodes, state.SubstrateNode{
		SwarmNodeID: "swarm-" + hostname, Hostname: hostname, State: "ready", Availability: "active",
		Labels: map[string]string{state.LabelNodeID: platformID},
	})
}

// assertCode 断言 err 是指定码的 apperr 信封。
func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code() != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

// TestResolveAmbiguousHostnameIsInvalid：两个节点同名 → 显示名歧义 →
// E_PLACEMENT_NODE_INVALID 422 + 提示改用平台 ID。
func TestResolveAmbiguousHostnameIsInvalid(t *testing.T) {
	h := newHarness(t)
	h.addWorker("srv-01", "n_"+ulid.Make().String()) // 与本机 hostname 同名
	in := h.input()
	in.LabelRef = "srv-01"
	_, err := h.resolver.Resolve(context.Background(), in)
	assertCode(t, err, "E_PLACEMENT_NODE_INVALID")
	if !strings.Contains(err.Error(), "platform node ID") {
		t.Fatalf("ambiguity message must hint the platform ID, got: %s", err)
	}
	// 平台 ID 形态不歧义：逐一可解析。
	in.LabelRef = h.platformID
	d, err := h.resolver.Resolve(context.Background(), in)
	if err != nil || d.PlatformNodeID != h.platformID {
		t.Fatalf("resolve by platform ID = %+v err=%v", d, err)
	}
}

// TestAutoPickThreeFactors 三因子逐位验证（multi-node §2.6/D-MN-7）：
//   - 第一位 数据引力：卷注册表所在节点优先；
//   - 第二位 已钉数少：placements 权威计数；
//   - 第三位 平台 ID 字典序。
func TestAutoPickThreeFactors(t *testing.T) {
	ctx := context.Background()

	t.Run("data gravity wins", func(t *testing.T) {
		h := newHarness(t)
		wID := "n_" + ulid.Make().String()
		h.addWorker("worker-01", wID)
		// 预置卷注册表：数据在 worker（权威 SQLite，非观测缓存）。
		if _, _, err := h.store.RegisterAppVolume(ctx, state.VolumeWrite{
			AppID: h.appID, Key: "data", Name: "fleetly-my-api-data-aaaaaaaa",
			PlatformNodeID: wID, MountPath: "/var/lib/data",
		}); err != nil {
			t.Fatalf("register volume: %v", err)
		}
		d, err := h.resolver.Resolve(ctx, h.input())
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if d.PlatformNodeID != wID {
			t.Fatalf("picked %s, want data-gravity node %s", d.PlatformNodeID, wID)
		}
	})

	t.Run("fewer pinned apps wins", func(t *testing.T) {
		h := newHarness(t)
		wID := "n_" + ulid.Make().String()
		h.addWorker("worker-01", wID)
		// 同名兄弟应用：把 worker 的已钉数垫高（self: 1 钉 / worker: 2 钉）。
		other, err := testsupport.SeedAppE(t, h.store, "sibling")
		if err != nil {
			t.Fatalf("create sibling: %v", err)
		}
		if _, err := h.store.BindPlacement(ctx, state.PlacementWrite{
			AppID: other.ID, PlatformNodeID: wID, Source: state.PlacementSourcePlatform,
		}); err != nil {
			t.Fatalf("bind sibling: %v", err)
		}
		d, err := h.resolver.Resolve(ctx, h.input())
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if d.PlatformNodeID != h.platformID {
			t.Fatalf("picked %s, want the less-pinned node (self) %s", d.PlatformNodeID, h.platformID)
		}
	})

	t.Run("platform id order breaks ties", func(t *testing.T) {
		h := newHarness(t)
		// 候选池重建：两个无重力、零钉的裸 worker，字典序决胜；本机不 ready
		// → 退出候选池，剩余两 worker 对称。
		wA := "n_" + ulid.Make().String()
		wB := "n_" + ulid.Make().String()
		if wA > wB {
			wA, wB = wB, wA
		}
		h.setSelf("down", "active",
			state.SubstrateNode{SwarmNodeID: "swarm-worker-a", Hostname: "worker-a",
				State: "ready", Availability: "active", Labels: map[string]string{state.LabelNodeID: wA}},
			state.SubstrateNode{SwarmNodeID: "swarm-worker-b", Hostname: "worker-b",
				State: "ready", Availability: "active", Labels: map[string]string{state.LabelNodeID: wB}},
		)
		d, err := h.resolver.Resolve(ctx, h.input())
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if d.PlatformNodeID != wA {
			t.Fatalf("picked %s, want lexicographic winner %s", d.PlatformNodeID, wA)
		}
	})
}

// TestAutoPickIgnoresUnanchoredAndDrained：未锚定与 drain/pause 节点不入
// 候选池（anchored + ready + active）；全池为空 → E_PLACEMENT_NO_ELIGIBLE_NODE。
func TestAutoPickIgnoresUnanchoredAndDrained(t *testing.T) {
	h := newHarness(t)
	// 未锚定 worker（无 label）+ drain worker + 本机 down：三者均不合格。
	h.docker.nodes = append(h.docker.nodes,
		state.SubstrateNode{SwarmNodeID: "swarm-u", Hostname: "unanchored",
			State: "ready", Availability: "active", Labels: map[string]string{}},
		state.SubstrateNode{SwarmNodeID: "swarm-d", Hostname: "drained",
			State: "ready", Availability: "drain",
			Labels: map[string]string{state.LabelNodeID: "n_" + ulid.Make().String()}},
	)
	h.setSelf("down", "active", h.docker.nodes[1:]...)
	_, err := h.resolver.Resolve(context.Background(), h.input())
	assertCode(t, err, "E_PLACEMENT_NO_ELIGIBLE_NODE")
}
