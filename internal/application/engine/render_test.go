package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// TestRenderWorkloadsMatchesEnsure：渲染与部署投影单源（ADR-0071 决策 3）
// ——同一条已成功 Revision，RenderWorkloads 的输出与 materialize 下发到
// Runtime 的载体在用户面字段上逐一致；分野面恰是"渲染非下发"的三个
// 字段（Generation/GenerationScoped——代戳归 Ensure；Materials——渲染
// 不解材料，凭证永不进视图）。
func TestRenderWorkloadsMatchesEnsure(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()

	require.NoError(t, e.repos.Volumes.Create(ctx, &volume.Volume{
		ID: "01JD0VOL00000000000000000", ProjectID: tProjectID, Name: "data",
	}))
	volSpec := `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":2,"volumes":[{"volume_id":"data"}]}]}`
	rev1 := freezeSpec(t, e, 1, volSpec)
	deployToSucceeded(t, e, rev1)
	ensured := lastEnsure(rt)

	rev, ws, err := e.RenderWorkloads(ctx, tAppID, 0)
	require.NoError(t, err)
	assert.Equal(t, rev1, rev.ID, "effective default must resolve to the succeeded revision")
	require.Len(t, ws, 1)

	rendered := ws[0]
	dispatched := ensured.Spec["web"]
	// 渲染分野面归一后逐字段一致（单源 by construction，此断言钉回归）。
	rendered.Generation, rendered.GenerationScoped = 0, false
	dispatched.Generation, dispatched.GenerationScoped = 0, false
	assert.Equal(t, dispatched, rendered)
	// 卷钉住只读并入：部署路径 pinVolumes 落的钉与渲染路径读到的钉同源。
	require.Len(t, rendered.Placement.NodeIDs, 1, "render must carry the merged volume pin")
	assert.Equal(t, dispatched.Placement.NodeIDs, rendered.Placement.NodeIDs)
	assert.Nil(t, ws[0].Materials, "render must never resolve materials")
}

// TestRenderWorkloadsEffectiveDefault：seq=0 = 最新成功部署的 to_revision
// （ADR-0071 决策 2），显式 seq 可取历史；无成功部署的缺省寻址精确
// ErrNotFound（"最新冻结≠生效"的分野锚）。
func TestRenderWorkloadsEffectiveDefault(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()

	rev1 := freezeSpec(t, e, 1, imageSpecFor("nginx:1.27"))
	rev2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.28"))

	// 冻结未部署：缺省寻址拒绝，显式 seq 仍可渲染（声明面可读）。
	_, _, err := e.RenderWorkloads(ctx, tAppID, 0)
	require.True(t, errors.Is(err, state.ErrNotFound), err)
	rev, _, err := e.RenderWorkloads(ctx, tAppID, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), rev.Seq)

	deployToSucceeded(t, e, rev1)
	deployToSucceeded(t, e, rev2)

	rev, _, err = e.RenderWorkloads(ctx, tAppID, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(2), rev.Seq, "default must track the latest succeeded deployment")
	rev, _, err = e.RenderWorkloads(ctx, tAppID, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), rev.Seq, "explicit seq renders historical revisions")
	assert.Equal(t, rev1, rev.ID)

	// 未知 seq / 未知 App 同归 ErrNotFound（api 面 map E_NOT_FOUND）。
	_, _, err = e.RenderWorkloads(ctx, tAppID, 99)
	assert.True(t, errors.Is(err, state.ErrNotFound), err)
	_, _, err = e.RenderWorkloads(ctx, "01JD0APP0000000000000MISS", 0)
	assert.True(t, errors.Is(err, state.ErrNotFound), err)
}

// TestInspectAppWorkloads：观测层透出（ADR-0071 观测层）——按 App 域取
// RuntimeInspector 观测，WorkloadID 归属过滤；未知 App 精确 ErrNotFound。
func TestInspectAppWorkloads(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()

	rev1 := freezeSpec(t, e, 1, tImageSpec)
	deployToSucceeded(t, e, rev1)

	obs, err := e.InspectAppWorkloads(ctx, tAppID)
	require.NoError(t, err)
	require.Len(t, obs, 1)
	assert.Equal(t, WorkloadID(tAppID, "web"), obs[0].WorkloadID)

	_, err = e.InspectAppWorkloads(ctx, "01JD0APP0000000000000MISS")
	assert.True(t, errors.Is(err, state.ErrNotFound), err)
}

// inspectlessRuntime 逐方法委托 fakeRuntime 但方法集不含 InspectWorkloads
// （嵌入会提升该方法，无法遮蔽——第三 runtime 缺席观测子面的形态）。
type inspectlessRuntime struct{ inner *fakeRuntime }

func (r *inspectlessRuntime) Describe() capability.ProviderDescriptor { return r.inner.Describe() }
func (r *inspectlessRuntime) Health(ctx context.Context) capability.HealthReport {
	return r.inner.Health(ctx)
}
func (r *inspectlessRuntime) Ensure(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, m capability.Materials) (capability.EnsureReport, error) {
	return r.inner.Ensure(ctx, ns, ws, gen, m)
}
func (r *inspectlessRuntime) Remove(ctx context.Context, ns capability.NamespaceRef) error {
	return r.inner.Remove(ctx, ns)
}
func (r *inspectlessRuntime) Watch(ctx context.Context) (<-chan capability.WorkloadEvent, error) {
	return r.inner.Watch(ctx)
}
func (r *inspectlessRuntime) Addresses(ctx context.Context, ns capability.NamespaceRef, expected []capability.Workload) ([]capability.Endpoint, error) {
	return r.inner.Addresses(ctx, ns, expected)
}
func (r *inspectlessRuntime) DescribeCluster(ctx context.Context) (capability.ClusterView, error) {
	return r.inner.DescribeCluster(ctx)
}
func (r *inspectlessRuntime) Enrollment(ctx context.Context, initial bool, opts capability.EnrollmentOptions) (capability.EnrollKit, error) {
	return r.inner.Enrollment(ctx, initial, opts)
}

// TestInspectAppWorkloadsRequiresRuntimeSubface：Runtime 未实现观测子面 →
// 精确拒绝（不静默空集——排障面拿到空集会误诊"什么都没跑"）。
func TestInspectAppWorkloadsRequiresRuntimeSubface(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	e.runtime = &inspectlessRuntime{inner: rt}
	_, err := e.InspectAppWorkloads(context.Background(), tAppID)
	assert.True(t, errors.Is(err, ErrRuntimeNoInspect), err)
}
