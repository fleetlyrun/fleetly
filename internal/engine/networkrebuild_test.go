package engine

// 网络重建动词单测（ADR-0046）：归属裁决的域轴面（受管域/Task 轴通过、
// 失锚拒绝）、重建期间零假 drift 事件（ADR-0046 决策 3 的执法锚）、与
// 部署 Ensure 的串行化（maintenanceMu 排队语义）。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// seedDefaultNetworkRow 落 networks 表行（重建受理面）。
func seedDefaultNetworkRow(t *testing.T, e *Engine) {
	t.Helper()
	require.NoError(t, networkrepo.New(e.db.Clock()).Create(context.Background(), e.db.Runner(),
		&networkrepo.Network{ID: ulid.Make().String(), ProjectID: tProjectID, Name: "default"}))
}

// lowerDomain 把域轴值小写化（模拟载体标记的 sanitizeNamePart 形态）。
func lowerDomain(ns capability.NamespaceRef) capability.NamespaceRef {
	return capability.NamespaceRef{
		Team:     strings.ToLower(ns.Team),
		Project:  strings.ToLower(ns.Project),
		App:      strings.ToLower(ns.App),
		Task:     strings.ToLower(ns.Task),
		Database: strings.ToLower(ns.Database),
	}
}

// ensureCountOf 统计指定 App 的 Ensure 次数（串行化断言面）。
func ensureCountOf(t *testing.T, rt *fakeRuntime, appID string) int {
	t.Helper()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	n := 0
	for _, c := range rt.ensures {
		if c.NS.App == appID {
			n++
		}
	}
	return n
}

// 归属裁决的域轴面：受管域载体（在册 Managed Provider 命名空间）与
// Task 轴载体（driving 行）通过；App 轴失锚（行不存在）拒绝并列出。
func TestRebuildNetworkAttributionAxes(t *testing.T) {
	db, clock := statertest.New(t)
	rt := newFakeRuntime()
	// 受管 reconciler 在册（fakeRegistry 的命名空间 = fleetly/system）。
	e := New(Deps{DB: db, Runtime: rt, Registry: newFakeRegistry(), Logger: discardLogger()}, Options{})
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	require.NoError(t, project.New(clock).Create(context.Background(), db.Runner(), &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	seedDefaultNetworkRow(t, e)

	// driving Task 行（Task 轴附着的期望锚；spec 冻结体最小形态）。
	taskID := ulid.Make().String()
	require.NoError(t, task.New(clock).Create(context.Background(), db.Runner(), &task.Task{
		ID: taskID, ProjectID: tProjectID, Name: "migrate", State: task.StateActive,
		Spec: []byte(`{"schema_version":1,"task":{"id":"` + taskID + `","project":"` + tProjectID + `"},` +
			`"process":{"name":"run","image":"busybox:1.37"},"desired_concurrency":1,"form":"oneshot"}`),
	}))

	ns := capability.NamespaceRef{Team: "default", Project: tProjectID}
	ghost := ulid.Make().String()
	rt.seedNetworkCarrier(ns, "default", false,
		capability.NetworkAttachment{Carrier: "fleetly-run-1", Domain: lowerDomain(capability.NamespaceRef{Team: "default", Project: tProjectID, Task: taskID})},
		capability.NetworkAttachment{Carrier: "fleetly-fleetly-system-registry", Workload: "fleetly-registry", Domain: lowerDomain(capability.NamespaceRef{Team: "fleetly", Project: "system", App: "registry"})},
		capability.NetworkAttachment{Carrier: "fleetly-app-ghost", Domain: lowerDomain(capability.NamespaceRef{Team: "default", Project: tProjectID, App: ghost})},
	)

	_, err := e.RebuildNetwork(context.Background(), tProjectID, "default")
	require.Error(t, err)
	var foreign *ForeignAttachmentError
	require.ErrorAs(t, err, &foreign)
	assert.Equal(t, []string{"fleetly-app-ghost (app " + strings.ToLower(ghost) + " is not an active app)"}, foreign.Carriers)
	// 拒绝即零动作：只有 inspect 流水。
	assert.Equal(t, []string{"inspect " + fakeNetKey(ns, "default")}, rt.maintFlow())

	// 摘掉失锚附着后重跑：两轴全通过。
	rt.seedNetworkCarrier(ns, "default", false,
		capability.NetworkAttachment{Carrier: "fleetly-run-1", Domain: lowerDomain(capability.NamespaceRef{Team: "default", Project: tProjectID, Task: taskID})},
		capability.NetworkAttachment{Carrier: "fleetly-fleetly-system-registry", Workload: "fleetly-registry", Domain: lowerDomain(capability.NamespaceRef{Team: "fleetly", Project: "system", App: "registry"})},
	)
	res, err := e.RebuildNetwork(context.Background(), tProjectID, "default")
	require.NoError(t, err)
	assert.Equal(t, 2, res.Detached)
	assert.Equal(t, 2, res.Reattached)
	assert.True(t, rt.netAttachable(ns, "default"))
}

// 重建期间零假 drift 事件（ADR-0046 决策 3）：detach/re-attach 不动 spec
// 对照面（Image/Command/Replicas，ADR-0022 字段冻结）——重建前后扫描拍
// 不得产 workload.drift_detected；对照面：真镜像失配仍要报（防回归不
// 等于放松执法）。幂等快速路径零扰动同测。
func TestRebuildNetworkZeroDriftDuringWindow(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	seedDefaultNetworkRow(t, e)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	ns := capability.NamespaceRef{Team: "default", Project: tProjectID}
	// legacy 形态 + 部署过的 App 载体附着。
	rt.seedNetworkCarrier(ns, "default", false,
		capability.NetworkAttachment{Carrier: "fleetly-default-shop-web-web", Domain: lowerDomain(capability.NamespaceRef{Team: "default", Project: tProjectID, App: tAppID})},
	)

	// 重建前扫一拍（detach 窗前基线：零 drift）。
	e.driftScan(context.Background())
	res, err := e.RebuildNetwork(context.Background(), tProjectID, "default")
	require.NoError(t, err)
	assert.Equal(t, 1, res.Detached)
	// 重建后连扫两拍（attach 完成拍 + 去抖拍）：零 drift。
	e.driftScan(context.Background())
	e.driftScan(context.Background())
	for _, name := range eventNames(t, e, tAppID+"-web") {
		require.NotEqual(t, "workload.drift_detected", name,
			"carrier network detach/re-attach must not raise drift (spec compare reads image/command/replicas only, ADR-0022/0046)")
	}

	// 对照面：真镜像失配仍要报。
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.28"}}
	rt.mu.Unlock()
	e.driftScan(context.Background())
	assert.Equal(t, []string{"workload.drift_detected"}, eventNames(t, e, tAppID+"-web"),
		"a real image mismatch must still raise drift")

	// 幂等快速路径：已 attachable → 零维护原语扰动（只追加一次 inspect）。
	flowl := len(rt.maintFlow())
	res, err = e.RebuildNetwork(context.Background(), tProjectID, "default")
	require.NoError(t, err)
	assert.Equal(t, 0, res.Detached)
	assert.Equal(t, 0, res.Reattached)
	assert.Equal(t, flowl+1, len(rt.maintFlow()), "fast path issues exactly one inspect")
}

// 串行化执法锚（ADR-0046 决策 3）：重建持写锁期间，部署链的 Ensure
// （materialize 读锁）排队不并行——detach 窗内无 Ensure 互踩；释放后
// 排队的部署照常收敛。
func TestRebuildNetworkSerializesWithDeployEnsure(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	seedDefaultNetworkRow(t, e)
	rev := freezeSpec(t, e, 1, tImageSpec)

	ns := capability.NamespaceRef{Team: "default", Project: tProjectID}
	rt.seedNetworkCarrier(ns, "default", false,
		capability.NetworkAttachment{Carrier: "fleetly-default-shop-web-web", Domain: lowerDomain(capability.NamespaceRef{Team: "default", Project: tProjectID, App: tAppID})},
	)

	// detach 卡点：重建进入 detach 后持写锁等待释放。
	entered := make(chan struct{})
	release := make(chan struct{})
	rt.setMaintHook(func(op string) {
		if strings.HasPrefix(op, "detach ") {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}
	})

	rebuilt := make(chan error, 1)
	go func() {
		_, err := e.RebuildNetwork(context.Background(), tProjectID, "default")
		rebuilt <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("rebuild never reached the detach step")
	}

	// 重建持锁期间受理部署并驱动：materialize 的 Ensure 必须排队（不进入
	// runtime.Ensure——detach 窗内无互踩）。驱动步本身在后台 goroutine
	//（RLock 排队阻塞是断言前提，不能占本 goroutine）。
	d, _, err := e.Submit(context.Background(), SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	stepDone := make(chan struct{})
	go func() {
		e.step(context.Background())
		close(stepDone)
	}()
	select {
	case <-stepDone:
		t.Fatal("the drive step must queue behind the rebuild write lock, not complete")
	case <-time.After(150 * time.Millisecond):
	}
	assert.Equal(t, 0, ensureCountOf(t, rt, tAppID), "ensure must queue behind the rebuild write lock")
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d.ID).State,
		"the deployment stays in releasing (pre-ensure) while the rebuild holds the write lock")

	close(release)
	require.NoError(t, <-rebuilt)
	// 释放后：排队的驱动步完成，部署照常收敛（Ensure 已入）。
	select {
	case <-stepDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued drive step never completed after the rebuild released the lock")
	}
	assert.GreaterOrEqual(t, ensureCountOf(t, rt, tAppID), 1, "queued ensure proceeds after the rebuild releases the lock")
}

// 串行化执法锚（项目删除级联批，2026-10-05）：重建持写锁期间，
// TeardownDatabase 的 Remove（读锁）排队不并行——detach 窗内不得插入
// 拆库载体（半拆网与半拆库交错会把重建的 re-attach 面对已逝归属）；
// 释放后排队的拆除照常收口。
func TestRebuildNetworkSerializesWithDatabaseTeardown(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, rt, _ := newDatabaseFixture(t, "postgres", url)

	ns := capability.NamespaceRef{Team: "default", Project: tProjectID}
	rt.seedNetworkCarrier(ns, "default", false,
		capability.NetworkAttachment{
			Carrier: "fleetly-db-" + tDatabaseID,
			Domain:  lowerDomain(capability.NamespaceRef{Team: "default", Project: tProjectID, Database: tDatabaseID}),
		},
	)

	// detach 卡点：重建进入 detach 后持写锁等待释放。
	entered := make(chan struct{})
	release := make(chan struct{})
	rt.setMaintHook(func(op string) {
		if strings.HasPrefix(op, "detach ") {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}
	})

	rebuilt := make(chan error, 1)
	go func() {
		_, err := e.RebuildNetwork(context.Background(), tProjectID, "default")
		rebuilt <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("rebuild never reached the detach step")
	}

	// 重建持锁期间启动库拆除（项目删除级联的收口动词）：Remove 必须排队
	// （不进 runtime.Remove——detach 窗内无互踩）。拆除在后台 goroutine
	//（RLock 排队阻塞是断言前提，不能占本 goroutine）。
	torn := make(chan error, 1)
	go func() {
		torn <- e.TeardownDatabase(context.Background(), tDatabaseID)
	}()
	select {
	case <-torn:
		t.Fatal("the database teardown must queue behind the rebuild write lock, not complete")
	case <-time.After(150 * time.Millisecond):
	}
	assert.NotContains(t, rt.removedSnapshot(), capability.NamespaceRef{
		Team: "default", Project: tProjectID, Database: tDatabaseID,
	}, "database carrier removal must queue behind the rebuild write lock")

	close(release)
	require.NoError(t, <-rebuilt)
	// 释放后：排队的拆除完成，载体收口。
	select {
	case <-torn:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued teardown never completed after the rebuild released the lock")
	}
	assert.Contains(t, rt.removedSnapshot(), capability.NamespaceRef{
		Team: "default", Project: tProjectID, Database: tDatabaseID,
	}, "queued teardown proceeds after the rebuild releases the lock")
}
