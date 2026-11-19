package apitest_test

// Functions 受理面 API 契约（ADR-0102 任务④；设计 v3 §5/§10）：创建即首
// 部署（行 + 冻结体 + 换轨原子）、三平面独立动词（scale/exposure 行写零
// 重部署）、回滚 Replay 复用行、删除级联（载体拆除 + revisions 终局全清，
// ADR-0101）、配额与受理位 fail-closed 面（零网项目/出界 scale/公共 host
// 占用）。夹具走生产装配通道（NewAPIDeps→RegisterAll——FunctionFactory
// 与生产同源）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// newFunctionProject 建项目（ProjectsService.CreateProject 受理即落默认
// 网络——活跃网络在场前置天然满足；每测试新库，项目名无跨测试冲突）。
func newFunctionProject(t *testing.T, h *apitest.Harness, ctx context.Context) string {
	t.Helper()
	_ = ctx
	return h.CreateProject(t, "functions").GetId()
}

func createTestFunction(t *testing.T, h *apitest.Harness, ctx context.Context, projectID, name string) *deliveryv1.Function {
	t.Helper()
	fns := deliveryv1.NewFunctionsServiceClient(h.Conn)
	resp, err := fns.CreateFunction(ctx, &deliveryv1.CreateFunctionRequest{
		ProjectId: projectID, Name: name, Image: "ghcr.io/fleetly/http-echo:1", Port: 8080,
		MinReplicas: 1, MaxReplicas: 4,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetFunction())
	return resp.GetFunction()
}

func TestFunctionsLifecycle(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	fns := deliveryv1.NewFunctionsServiceClient(h.Conn)
	projectID := newFunctionProject(t, h, ctx)

	fn := createTestFunction(t, h, ctx, projectID, "resize-img")
	assert.NotEmpty(t, fn.GetId())
	assert.Equal(t, "fn-"+fn.GetId(), fn.GetDnsName(), "fn-<id> is derived from the row id")
	assert.Contains(t, fn.GetPrivateUrl(), fn.GetDnsName())
	assert.Equal(t, "private", fn.GetExposure())
	assert.Equal(t, int64(1), fn.GetCurrentRevisionSeq(), "create is the first deploy")

	// 同项目同名活跃 → E_ALREADY_EXISTS 形（映射后语义见错误串）。
	_, err := fns.CreateFunction(ctx, &deliveryv1.CreateFunctionRequest{
		ProjectId: projectID, Name: "resize-img", Image: "img", Port: 8080,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ALREADY_EXISTS")

	// Get/List。
	got, err := fns.GetFunction(ctx, &deliveryv1.GetFunctionRequest{Id: fn.GetId()})
	require.NoError(t, err)
	assert.Equal(t, fn.GetId(), got.GetFunction().GetId())
	list, err := fns.ListFunctions(ctx, &deliveryv1.ListFunctionsRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, list.GetFunctions(), 1)

	// Deploy 全量替换：换轨到 r2。
	dep, err := fns.DeployFunction(ctx, &deliveryv1.DeployFunctionRequest{
		FunctionId: fn.GetId(), Image: "ghcr.io/fleetly/http-echo:2", Port: 8080, Command: []string{"/app", "-v=2"},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), dep.GetFunction().GetCurrentRevisionSeq())

	// Rollback 缺省：回上一枚（r1）——Replay 复用行（行数不增断言在
	// ListFunctionRevisions 面：恰 2 枚冻结体）。
	rb, err := fns.RollbackFunction(ctx, &deliveryv1.RollbackFunctionRequest{FunctionId: fn.GetId()})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rb.GetFunction().GetCurrentRevisionSeq())
	revs, err := fns.ListFunctionRevisions(ctx, &deliveryv1.ListFunctionRevisionsRequest{FunctionId: fn.GetId()})
	require.NoError(t, err)
	assert.Len(t, revs.GetRevisions(), 2, "rollback replays the frozen row — no new revision")

	// Scale：界内行 CAS；出界拒（受理位单点执法）。
	sc, err := fns.ScaleFunction(ctx, &deliveryv1.ScaleFunctionRequest{FunctionId: fn.GetId(), Replicas: 3})
	require.NoError(t, err)
	assert.Equal(t, int64(3), sc.GetFunction().GetReplicas())
	_, err = fns.ScaleFunction(ctx, &deliveryv1.ScaleFunctionRequest{FunctionId: fn.GetId(), Replicas: 9})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")

	// Exposure：public 需 host；占用冲突 E_CONFLICT；双向转换。
	_, err = fns.SetFunctionExposure(ctx, &deliveryv1.SetFunctionExposureRequest{FunctionId: fn.GetId(), Exposure: "public"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")
	pub, err := fns.SetFunctionExposure(ctx, &deliveryv1.SetFunctionExposureRequest{
		FunctionId: fn.GetId(), Exposure: "public", Host: "img.functions.test",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://img.functions.test", pub.GetFunction().GetPublicUrl())
	other := createTestFunction(t, h, ctx, projectID, "second-fn")
	_, err = fns.SetFunctionExposure(ctx, &deliveryv1.SetFunctionExposureRequest{
		FunctionId: other.GetId(), Exposure: "public", Host: "img.functions.test",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CONFLICT", "host occupancy precheck")
	// 转回 private：host 清位、public_url 消失（双向转换三律）。
	priv, err := fns.SetFunctionExposure(ctx, &deliveryv1.SetFunctionExposureRequest{
		FunctionId: fn.GetId(), Exposure: "private",
	})
	require.NoError(t, err)
	assert.Empty(t, priv.GetFunction().GetPublicUrl())
	assert.NotEmpty(t, priv.GetFunction().GetPrivateUrl(), "fn-<id> private name survives the flip")

	// 删除级联（ADR-0101 决策 4）：载体拆除 + revisions 终局全清 + tombstone。
	_, err = fns.DeleteFunction(ctx, &deliveryv1.DeleteFunctionRequest{Id: fn.GetId()})
	require.NoError(t, err)
	assert.NotEmpty(t, h.Runtime.Removed(), "delete must tear down runtime carriers")
	_, err = fns.GetFunction(ctx, &deliveryv1.GetFunctionRequest{Id: fn.GetId()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
	// 幂等 tombstone：再删同形 404。
	_, err = fns.DeleteFunction(ctx, &deliveryv1.DeleteFunctionRequest{Id: fn.GetId()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
}

func TestFunctionsAcceptanceFailClosed(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	fns := deliveryv1.NewFunctionsServiceClient(h.Conn)

	// 零网项目：private 不可达即拒（fail-closed 优于静默孤岛）——项目受理
	// 即落默认网络且无删网动词，零网态经 DB 直构（受理检查防的正是该态）。
	bare := h.CreateProject(t, "bare").GetId()
	_, err := h.DB.Runner().ExecContext(ctx,
		`DELETE FROM networks WHERE project_id = ?`, bare)
	require.NoError(t, err)
	_, err = fns.CreateFunction(ctx, &deliveryv1.CreateFunctionRequest{
		ProjectId: bare, Name: "fn", Image: "img", Port: 8080,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CONFLICT")

	// secret_refs 缺失：精确 E_NOT_FOUND。
	projectID := newFunctionProject(t, h, ctx)
	_, err = fns.CreateFunction(ctx, &deliveryv1.CreateFunctionRequest{
		ProjectId: projectID, Name: "wants-secret", Image: "img", Port: 8080, SecretRefs: []string{"missing"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
	assert.Contains(t, err.Error(), "missing")

	// min > max：E_INVALID_ARGUMENT。
	_, err = fns.CreateFunction(ctx, &deliveryv1.CreateFunctionRequest{
		ProjectId: projectID, Name: "bad-window", Image: "img", Port: 8080, MinReplicas: 4, MaxReplicas: 1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")
}

func TestFunctionsQuota(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	fns := deliveryv1.NewFunctionsServiceClient(h.Conn)
	projectID := newFunctionProject(t, h, ctx)
	// 三枚单副本（sum=3）+ 三枚 16 副本：3+16×3=51 过；51+16=67 超
	// replicas 合上限 64——受理位事务内拒（E_QUOTA_EXCEEDED）。
	for i := 0; i < 3; i++ {
		createTestFunction(t, h, ctx, projectID, "q-fn-"+string(rune('a'+i)))
	}
	big := &deliveryv1.CreateFunctionRequest{
		ProjectId: projectID, Image: "img", Port: 8080, MinReplicas: 16, MaxReplicas: 16,
	}
	for i := 0; i < 3; i++ {
		big.Name = "big-fn-" + string(rune('a'+i))
		_, err := fns.CreateFunction(ctx, big)
		require.NoError(t, err)
	}
	big.Name = "over-fn"
	_, err := fns.CreateFunction(ctx, big)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_QUOTA_EXCEEDED", "replica-sum quota is enforced at the acceptance gate")
}

func TestFunctionsRollbackOutsideRetentionWindow(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	fns := deliveryv1.NewFunctionsServiceClient(h.Conn)
	projectID := newFunctionProject(t, h, ctx)
	fn := createTestFunction(t, h, ctx, projectID, "retention-fn")

	_, err := fns.RollbackFunction(ctx, &deliveryv1.RollbackFunctionRequest{
		FunctionId: fn.GetId(), RevisionSeq: 999,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
	env, ok := apperrstatus.FromError(err)
	require.True(t, ok)
	assert.Contains(t, env.Suggestion(), "retention policy", "outside-window rollback carries the retention suggestion")
}

// P2-1a：min=0 值域与唤醒面——创建即常驻 1（首副本 max(1,min)）、scale 0
// 仅对 min=0 函数合法（门在受理位）、parked 显示映射（replicas=0 →
// status "parked"，行事实优先于过期观测缓存）。
func TestFunctionsScaleToZeroDomain(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	fns := deliveryv1.NewFunctionsServiceClient(h.Conn)
	projectID := newFunctionProject(t, h, ctx)

	// min=0 创建：首副本 = max(1, 0) = 1（缩零由决策环驱动，不在创建面）。
	zero, err := fns.CreateFunction(ctx, &deliveryv1.CreateFunctionRequest{
		ProjectId: projectID, Name: "parkable", Image: "img", Port: 8080,
		MinReplicas: 0, MaxReplicas: 4,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), zero.GetFunction().GetReplicas(), "first replica = max(1, min)")
	assert.Equal(t, int64(0), zero.GetFunction().GetMinReplicas())

	// min=0 函数可显式 park（scale 0）→ status 显示 parked。
	parked, err := fns.ScaleFunction(ctx, &deliveryv1.ScaleFunctionRequest{
		FunctionId: zero.GetFunction().GetId(), Replicas: 0,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), parked.GetFunction().GetReplicas())
	assert.Equal(t, "parked", parked.GetFunction().GetStatus(), "row-fact display mapping wins over the observation cache")

	// min=1 函数 scale 0 → 精确拒（需先开窗）。
	one := createTestFunction(t, h, ctx, projectID, "windowed")
	_, err = fns.ScaleFunction(ctx, &deliveryv1.ScaleFunctionRequest{
		FunctionId: one.GetId(), Replicas: 0,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")
	assert.Contains(t, err.Error(), "scale-to-zero")
}
