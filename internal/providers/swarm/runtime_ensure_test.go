package swarm

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// ensureNS/ensureWorkload 是 Ensure 测试的输入夹具。
var (
	ensureNS = capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
)

func ensureWorkload() capability.Workload {
	return capability.Workload{ID: "wl_01", Process: "web-1", Image: "nginx:1.27"}
}

// ensureCarrierName 是上述夹具的载体名（workloadServiceName 公式）。
func ensureCarrierName() string {
	return workloadServiceName(ensureNS, ensureWorkload())
}

// TestEnsureNoopBreakerSkipsIdenticalDesired（C19-1）：daemon 版本漂移新增
// 物化字段（fake 以 mutate 叠加平台不管理的 label 模拟）使 serviceSpecEqual
// 恒不等——无断路器时每拍重发 update 自激复燃；断路器以"最近一次确认下发
// 的期望 canonical"为锚，期望不变即整条 update 判定链短路。并钉清账三时
// 点：update 失败整拍作废（下拍重发）、域内收敛删除清账。
func TestEnsureNoopBreakerSkipsIdenticalDesired(t *testing.T) {
	const materialized = "com.docker.materialized" // daemon 物化字段形态
	d := newFakeDaemon()
	d.mutate = func(spec *swarm.ServiceSpec) {
		if spec.Labels == nil {
			spec.Labels = map[string]string{}
		}
		spec.Labels[materialized] = "true" // serviceSpecEqual 恒不等的漂移源
	}
	p := &Provider{cli: d.newClient(t)}
	ctx := context.Background()
	name := ensureCarrierName()
	updateKey := "POST /services/srv-" + name + "/update"

	run := func() error {
		return p.Ensure(ctx, ensureNS, []capability.Workload{ensureWorkload()}, capability.Generation(1), capability.Materials{})
	}

	// 拍 1：服务不存在 → create 落账。
	require.NoError(t, run())
	assert.Equal(t, 1, d.count("POST /services/create"))
	assert.Equal(t, 0, d.count(updateKey))
	require.NotEmpty(t, p.lastIssuedOf(name), "create must seed the ledger")

	// 拍 2：期望不变（服务端 spec 因物化字段恒不等）→ 断路器短路，
	// 零 create 零 update。
	require.NoError(t, run())
	assert.Equal(t, 1, d.count("POST /services/create"), "existing service must not be recreated")
	assert.Equal(t, 0, d.count(updateKey), "identical desired must short-circuit despite server-side materialized drift")

	// 拍 3：期望真变（镜像升级）→ 短路解除，update 走通。
	changed := ensureWorkload()
	changed.Image = "nginx:1.28"
	require.NoError(t, p.Ensure(ctx, ensureNS, []capability.Workload{changed}, capability.Generation(2), capability.Materials{}))
	assert.Equal(t, 1, d.count(updateKey), "a real desired change must reach the daemon")

	// 拍 4：期望再回稳 → 断路器在新锚上短路。
	require.NoError(t, p.Ensure(ctx, ensureNS, []capability.Workload{changed}, capability.Generation(2), capability.Materials{}))
	assert.Equal(t, 1, d.count(updateKey), "steady state after a real change must stay quiet")

	// 拍 5：期望再变 + update 注入故障 → 账本整拍作废（含本拍与此前落账）。
	changed2 := ensureWorkload()
	changed2.Image = "nginx:1.29"
	d.failNext(updateKey, 1)
	err := p.Ensure(ctx, ensureNS, []capability.Workload{changed2}, capability.Generation(3), capability.Materials{})
	require.Error(t, err, "injected update failure must surface")
	assert.Empty(t, p.lastIssuedOf(name), "a failed beat must wipe the ledger (re-issue next beat)")

	// 拍 6：同期望重放 → 账本已作废，update 重发成功（故障已耗尽；计数含
	// 拍 5 的失败尝试）。
	require.NoError(t, p.Ensure(ctx, ensureNS, []capability.Workload{changed2}, capability.Generation(3), capability.Materials{}))
	assert.Equal(t, 3, d.count(updateKey), "the wiped ledger must re-issue the update (attempt count includes the failed beat)")
	require.NotEmpty(t, p.lastIssuedOf(name))

	// 拍 7：稳态回归静默。
	require.NoError(t, p.Ensure(ctx, ensureNS, []capability.Workload{changed2}, capability.Generation(3), capability.Materials{}))
	assert.Equal(t, 3, d.count(updateKey))

	// 域内收敛删除清账：期望集外的存量服务被移除且账本同步清空（存量载体
	// 带 ns 选择器全集——真服务由 Ensure 创建，标签齐全）。
	const staleName = "fleetly-acme-shop-web-old"
	stale := swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Name: staleName,
			Labels: map[string]string{
				labelManaged:  "true",
				labelTeam:     sanitizeNamePart(ensureNS.Team),
				labelProject:  sanitizeNamePart(ensureNS.Project),
				labelApp:      sanitizeNamePart(ensureNS.App),
				labelWorkload: "wl_old",
			},
		},
	}
	d.addService(stale)
	p.recordLastIssued(staleName, "stale-anchor")
	require.NoError(t, p.Ensure(ctx, ensureNS, []capability.Workload{changed2}, capability.Generation(3), capability.Materials{}))
	assert.Equal(t, 1, d.count("DELETE /services/srv-"+staleName), "stale service must be removed")
	assert.Empty(t, p.lastIssuedOf(staleName), "removal must forget the ledger entry")
}

// TestPollTasksListsManagedServicesPerService（C19-2）：任务轮询先按
// managed 标记列服务（量级 = 受管服务数），再逐服务带 service 过滤列任务；
// 单服务任务列表失败只记 Warn 降级该服务（带服务名），其余服务照常出观测，
// 整轮不报错。
func TestPollTasksListsManagedServicesPerService(t *testing.T) {
	d := newFakeDaemon()
	managed := func(id, name, wl string) swarm.Service {
		return swarm.Service{ID: id, Spec: swarm.ServiceSpec{
			Annotations: swarm.Annotations{Name: name, Labels: map[string]string{
				labelManaged:    "true",
				labelWorkload:   wl,
				labelGeneration: "7",
			}},
		}}
	}
	// 服务 a（排序在前）的任务列表注入一次故障；服务 z 正常。
	faulted := managed("srv-a", "fleetly-a-web", "wl_a")
	healthy := managed("srv-z", "fleetly-z-web", "wl_z")
	d.store[faulted.Spec.Name] = faulted
	d.store[healthy.Spec.Name] = healthy
	d.tasks["srv-z"] = []swarm.Task{{
		ID:           "task-z1",
		ServiceID:    "srv-z",
		DesiredState: swarm.TaskStateRunning,
		Status:       swarm.TaskStatus{State: swarm.TaskStateRunning},
	}}
	// 非受管服务：任务存在于集群（旧实现全集群 TaskList 会捞到），受管过
	// 滤后不得出现。
	unmanaged := swarm.Service{ID: "srv-x", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "outsider"}}}
	d.store[unmanaged.Spec.Name] = unmanaged
	d.tasks["srv-x"] = []swarm.Task{{
		ID:           "task-x1",
		ServiceID:    "srv-x",
		DesiredState: swarm.TaskStateRunning,
		Status:       swarm.TaskStatus{State: swarm.TaskStateRunning},
	}}
	d.failNext("GET /tasks", 1) // 首个 /tasks 调用（排序在前的 srv-a）失败

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p := &Provider{cli: d.newClient(t)}
	out := make(chan capability.WorkloadEvent, 8)
	require.NoError(t, p.pollTasks(context.Background(), out))
	close(out)

	// 服务列表走 managed 标记选择器。
	svcQueries := d.queriesOf("GET /services")
	require.Len(t, svcQueries, 1)
	assert.Contains(t, svcQueries[0].Get("filters"), labelManaged+"=true",
		"service listing must use the managed label selector")

	// 任务列表逐服务带 service 过滤（无全集群裸列）。
	taskQueries := d.queriesOf("GET /tasks")
	require.Len(t, taskQueries, 2, "one task list per managed service")
	for _, q := range taskQueries {
		f := decodeFilters(q)
		assert.Len(t, f["service"], 1, "task list must carry a per-service filter")
	}

	// 哨兵：失败服务记 Warn 带服务名，其余服务观测照常，整轮无错误。
	assert.Contains(t, logs.String(), "fleetly-a-web", "the skipped service must be named in the log")
	var evs []capability.WorkloadEvent
	for ev := range out {
		evs = append(evs, ev)
	}
	require.Len(t, evs, 1)
	assert.Equal(t, "wl_z", evs[0].WorkloadID, "the healthy service's observation must survive the sibling failure")
	assert.Equal(t, capability.Generation(7), evs[0].Generation)
	assert.Equal(t, capability.WorkloadRunning, evs[0].State)
	assert.NotContains(t, logs.String(), "outsider")
}

// TestResolveNetworkTargetsMemoAndFaults（C19-3）：引用名→ID 解析以
// per-Ensure 备忘去重（同名只探一次）；404 保留名字原样（既有语义）；非
// 404 错误上抛带原因（Q-20，不再吞）。
func TestResolveNetworkTargetsMemoAndFaults(t *testing.T) {
	const carrier = "fleetly-net-shop-edge"
	const netID = "net-edge-id"

	spec := func() swarm.ServiceSpec {
		return swarm.ServiceSpec{
			Annotations: swarm.Annotations{Name: "fleetly-acme-shop-web-web-1"},
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{Image: "nginx:1.27"},
				Networks:      []swarm.NetworkAttachmentConfig{{Target: carrier}},
			},
		}
	}

	t.Run("memo dedupes repeated names within one ensure", func(t *testing.T) {
		d := newFakeDaemon()
		d.nets[carrier] = netID
		p := &Provider{cli: d.newClient(t)}
		memo := map[string]netResolve{}
		first, err := p.resolveNetworkTargets(context.Background(), spec(), memo)
		require.NoError(t, err)
		second, err := p.resolveNetworkTargets(context.Background(), spec(), memo)
		require.NoError(t, err)
		assert.Equal(t, netID, first.TaskTemplate.Networks[0].Target)
		assert.Equal(t, netID, second.TaskTemplate.Networks[0].Target)
		assert.Equal(t, 1, d.count("GET /networks/"+carrier), "a repeated reference must hit the memo, not the daemon")
	})

	t.Run("not found keeps the carrier name", func(t *testing.T) {
		d := newFakeDaemon() // 无网络表 → 404
		p := &Provider{cli: d.newClient(t)}
		got, err := p.resolveNetworkTargets(context.Background(), spec(), map[string]netResolve{})
		require.NoError(t, err)
		assert.Equal(t, carrier, got.TaskTemplate.Networks[0].Target, "missing carrier keeps the name (server-side name error)")
	})

	t.Run("non-404 fault propagates", func(t *testing.T) {
		d := newFakeDaemon()
		d.nets[carrier] = netID
		d.failNext("GET /networks/"+carrier, 1)
		p := &Provider{cli: d.newClient(t)}
		_, err := p.resolveNetworkTargets(context.Background(), spec(), map[string]netResolve{})
		require.Error(t, err, "inspect failure must not be swallowed (Q-20)")
		assert.Contains(t, err.Error(), "resolve network "+carrier)
	})
}

// TestEnsureResolvesSharedNetworkOnce（C19-3 Ensure 级）：同拍两个 Workload
// 引用同一网络，材料期 inspect（create-or-get）+ 解析期 memo 共两次
// NetworkInspect——修复前解析期无 memo 为三次。
func TestEnsureResolvesSharedNetworkOnce(t *testing.T) {
	carrier := carrierNetworkName(ensureNS, "edge")
	d := newFakeDaemon()
	d.nets[carrier] = "net-edge-id"
	p := &Provider{cli: d.newClient(t)}
	w1, w2 := ensureWorkload(), ensureWorkload()
	w1.ID, w2.ID = "wl_01", "wl_02"
	w2.Process = "web-2"
	w1.Networks = []string{"edge"}
	w2.Networks = []string{"edge"}
	require.NoError(t, p.Ensure(context.Background(), ensureNS, []capability.Workload{w1, w2}, capability.Generation(1), capability.Materials{}))
	assert.Equal(t, 2, d.count("GET /networks/"+carrier),
		"materials inspect + one memoized resolve for two workloads sharing the network")
	for _, name := range []string{workloadServiceName(ensureNS, w1), workloadServiceName(ensureNS, w2)} {
		svc, ok := d.byRef(name)
		require.True(t, ok)
		require.NotEmpty(t, svc.Spec.TaskTemplate.Networks)
		assert.Equal(t, "net-edge-id", svc.Spec.TaskTemplate.Networks[0].Target, name)
	}
}

// TestDescribeClusterAnchorsWithoutEventSink（附带件）：观测对账路径锚定
// 新节点时无事件消费方——nil channel 上发送会阻塞到 ctx 取消（修复前
// DescribeCluster 对新节点必烧满整个调用预算并以失败告终）；修复后只铸
// 造写回锚定标记，调用按时返回且锚定生效。
func TestDescribeClusterAnchorsWithoutEventSink(t *testing.T) {
	d := newFakeDaemon()
	d.nodes = []swarm.Node{{
		ID:          "carrier-1",
		Meta:        swarm.Meta{Version: swarm.Version{Index: 3}},
		Description: swarm.NodeDescription{Hostname: "node-1"},
		Spec:        swarm.NodeSpec{Role: swarm.NodeRoleWorker},
	}}
	p := &Provider{cli: d.newClient(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	view, err := p.DescribeCluster(ctx)
	require.NoError(t, err, "the reconciliation anchor pass must not hang on the nil event sink")
	require.Len(t, view.Nodes, 1)
	assert.Equal(t, "carrier-1", view.Nodes[0].CarrierID)
	assert.NotEmpty(t, view.Nodes[0].NodeID, "the node must leave anchored (minted platform id visible)")
	assert.Equal(t, 1, d.count("POST /nodes/carrier-1/update"))
}
