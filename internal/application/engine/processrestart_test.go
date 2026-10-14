package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// processRestartRecorder 是 RuntimeRestart 子面的记录假件（ns + workloadID
// 双捕获；内嵌全量 fake——与 componentrestart 的 restartRecorder 分名共存）。
type processRestartRecorder struct {
	*fakeRuntime
	gotNS capability.NamespaceRef
	gotID string
}

func (r *processRestartRecorder) Restart(_ context.Context, ns capability.NamespaceRef, workloadID string) error {
	r.gotNS = ns
	r.gotID = workloadID
	return nil
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
	assert.Equal(t, tAppID+"-worker", rt.gotID)
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID, App: tAppID}, rt.gotNS)
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
