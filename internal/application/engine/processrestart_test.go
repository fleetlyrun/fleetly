package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// processRestartCall 是一次 Restart 调用记录（ns + 载体 ID 双捕获）。
type processRestartCall struct {
	ns capability.NamespaceRef
	id string
}

// processRestartRecorder 是 RuntimeRestart 子面的记录假件（内嵌全量
// fake——与 componentrestart 的 restartRecorder 分名共存）；errs 按载体 ID
// 注入未命中/失败（双形态回退与错误语义钉测）。
type processRestartRecorder struct {
	*fakeRuntime
	mu    sync.Mutex
	calls []processRestartCall
	errs  map[string]error
}

func (r *processRestartRecorder) Restart(_ context.Context, ns capability.NamespaceRef, workloadID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, processRestartCall{ns: ns, id: workloadID})
	return r.errs[workloadID]
}

// gotIDs 返回按调用序记录的载体 ID。
func (r *processRestartRecorder) gotIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		out = append(out, c.id)
	}
	return out
}

// twoProcessSpec 夹具：web + worker 双进程冻结体（restart 瞄准 worker）。
var twoProcessSpec = `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
	`"source":{"image":{"ref":"nginx:1.26"}},"processes":[` +
	`{"name":"web","image":"nginx:1.26","replicas":1},` +
	`{"name":"worker","image":"nginx:1.26","replicas":2}]}`

func TestRestartProcessTargetsWorkloadID(t *testing.T) {
	base := newFakeRuntime()
	rt := &processRestartRecorder{fakeRuntime: base}
	e, _, _ := newTestEngine(t)
	e.runtime = rt
	freezeSpec(t, e, 1, twoProcessSpec)

	err := e.RestartProcess(context.Background(), tAppID, "worker")
	require.NoError(t, err)
	require.Len(t, rt.calls, 1)
	assert.Equal(t, tAppID+"-worker", rt.calls[0].id)
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID, App: tAppID}, rt.calls[0].ns)
}

// R02-M3：BG 进程的载体 ID 是代次化形态（base-g<最新 succeeded Generation>）
// ——动作面重启按基线行解析，不以裸 ID 定位（swarm 未命中报错 / k3s 零命中
// 假成功）。
func TestRestartProcessTargetsGenerationScopedCarrierForBlueGreen(t *testing.T) {
	base := newFakeRuntime()
	rt := &processRestartRecorder{fakeRuntime: base}
	e, _, _ := newTestEngine(t)
	e.runtime = rt
	ctx := context.Background()
	rev1 := freezeSpec(t, e, 1, tImageSpec)
	deployToSucceeded(t, e, rev1)

	rev2 := freezeSpec(t, e, 2, bgSpecFor("nginx:1.28"))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx) // releasing：双代窗
	e.handleObservation(ctx, workloadEventRunning(genScopedWorkloadID(tAppID+"-web", d2.Generation), d2.Generation))
	e.step(ctx) // observing：切流
	advanceClock(t, e, 61*time.Second)
	e.step(ctx) // succeeded：新代成为基线
	require.Equal(t, "succeeded", string(getDeployment(t, e, d2.ID).State))

	require.NoError(t, e.RestartProcess(ctx, tAppID, "web"))
	require.Len(t, rt.calls, 1, "a blue-green process must be restarted through its generation-scoped carrier")
	assert.Equal(t, genScopedWorkloadID(tAppID+"-web", d2.Generation), rt.calls[0].id,
		"the carrier id is resolved from the latest succeeded deployment generation")

	// 重启后再次 restart 仍命中同一代次化载体（标签不随 ForceUpdate 漂移）。
	require.NoError(t, e.RestartProcess(ctx, tAppID, "web"))
	require.Len(t, rt.calls, 2)
	assert.Equal(t, rt.calls[0].id, rt.calls[1].id)
}

// 载体 ID 两种形态都尝试（裸 ID / 代次化）：首选未命中才回退，命中一次即停
// （不得双重重排）；两次都未命中 → 错误携带 state.ErrNotFound（E_NOT_FOUND
// 语义，k3s 零命中不得再假成功）。
func TestRestartProcessTriesBothCarrierForms(t *testing.T) {
	base := newFakeRuntime()
	rt := &processRestartRecorder{fakeRuntime: base, errs: map[string]error{}}
	e, _, _ := newTestEngine(t)
	e.runtime = rt
	ctx := context.Background()
	rev1 := freezeSpec(t, e, 1, tImageSpec)
	deployToSucceeded(t, e, rev1)

	// 非 BG 进程：首选裸 ID；裸 ID 未命中 → 回退代次化形态。
	rt.errs[tAppID+"-web"] = capability.ErrRestartTargetNotFound
	require.NoError(t, e.RestartProcess(ctx, tAppID, "web"))
	assert.Equal(t, []string{tAppID + "-web", genScopedWorkloadID(tAppID+"-web", 1)}, rt.gotIDs(),
		"the bare form is tried first for a rolling process, then the generation-scoped form")

	// 双形态都未命中：错误语义 = state.ErrNotFound（API 映射 E_NOT_FOUND）。
	rt2 := &processRestartRecorder{fakeRuntime: base, errs: map[string]error{
		tAppID + "-web":                       capability.ErrRestartTargetNotFound,
		genScopedWorkloadID(tAppID+"-web", 1): capability.ErrRestartTargetNotFound,
	}}
	e.runtime = rt2
	err := e.RestartProcess(ctx, tAppID, "web")
	require.Error(t, err)
	assert.True(t, errors.Is(err, state.ErrNotFound), "a carrier miss must surface the not-found sentinel, got %v", err)
	assert.Len(t, rt2.calls, 2, "both carrier forms are probed before failing")
}

func TestRestartProcessUnknownAndUndeployed(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()

	// 未部署（无冻结体）：同归 ErrProcessUnknown——无 Spec 即无进程。
	err := e.RestartProcess(ctx, tAppID, "web")
	require.True(t, errors.Is(err, ErrProcessUnknown), err)

	// 已部署但进程名不在冻结体（typo 防假成功——k3s 驱逐对零命中静默）。
	freezeSpec(t, e, 1, twoProcessSpec)
	err = e.RestartProcess(ctx, tAppID, "wrker")
	require.True(t, errors.Is(err, ErrProcessUnknown), err)

	// 空 app / 空进程名：参数面同哨兵。
	require.True(t, errors.Is(e.RestartProcess(ctx, "", "web"), ErrProcessUnknown))
	require.True(t, errors.Is(e.RestartProcess(ctx, tAppID, ""), ErrProcessUnknown))

	// 未知 App：appTeam 解析失败透传 state.ErrNotFound（api 面 map E_NOT_FOUND）。
	err = e.RestartProcess(ctx, "01JD0APP00000000000000MISS", "web")
	require.True(t, errors.Is(err, state.ErrNotFound), err)
}

func TestRestartProcessRequiresRuntimeSubface(t *testing.T) {
	// Runtime 不实现 RuntimeRestart（第三 runtime 缺席形态）→ 精确拒绝。
	e, _, _ := newTestEngine(t)
	freezeSpec(t, e, 1, twoProcessSpec)
	err := e.RestartProcess(context.Background(), tAppID, "web")
	assert.True(t, errors.Is(err, ErrRuntimeNoRestart), err)
}
