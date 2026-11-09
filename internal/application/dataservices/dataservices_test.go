package dataservices

// 分类型步白盒（ADR-0096 决策 1 环表三条目 / B2e 验收锚）：①注册表缺席的
// 类型（pubsub/bucketstore 在 B4b/B5 登记前）调 Step 恒 no-op——占位环零
// 载体写；②StepFor(KindDatabase) 与 Step(ctx, KindDatabase) 同路径
//（环表挂载形态 = 分类型步）；③在册而无收敛实现的类型 = panic（装配缺口
// fail-loud——loop.go「panic → ring dead」教义原样承接，不在卫星层 recover）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

func TestKindStepNoOpForUnregisteredKinds(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	ctx := context.Background()

	// B4b 起 pubsub、B5 起 bucketstore 类型在册（模板包初始化期登记——B2e
	// 占位环即活）。在册类型的空行集 Step 不触 Runtime（零行零 Ensure）。
	_, pubsubRegistered := dataservicetemplate.LookupKind(dataservicetemplate.KindPubSub)
	_, bucketRegistered := dataservicetemplate.LookupKind(dataservicetemplate.KindBucketStore)
	require.True(t, pubsubRegistered, "B4b: pubsub registers with pubsubtemplate")
	require.True(t, bucketRegistered, "B5: bucketstore registers with bucketstoretemplate")

	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	fx.d.Step(ctx, dataservicetemplate.KindBucketStore)
	fx.d.StepFor(dataservicetemplate.KindPubSub)(ctx)
	fx.d.Step(ctx, "never-a-kind")
	assert.Empty(t, fx.rt.calls(), "placeholder rings and empty registered rings must not touch the Runtime")
	assert.Empty(t, fx.ut.reqs, "placeholder rings must not run utility containers either")
}

func TestStepForBindsTheKindStep(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	ctx := context.Background()

	step := fx.d.StepFor(dataservicetemplate.KindDatabase)
	step(ctx)
	calls := fx.rt.calls()
	require.Len(t, calls, 1, "the database ring step converges the row through the kind step")
	assert.Equal(t, tDatabaseID, calls[0].Spec["postgres"].ID)

	// 同一分类型路径：第二拍经 Step 直调命中 memo 短路（Ensure 不再发）——
	// StepFor 闭包与 Step(ctx, kind) 共享类型状态面，而非第二份收敛器。
	fx.d.Step(ctx, dataservicetemplate.KindDatabase)
	assert.Len(t, fx.rt.calls(), 1, "signature short-circuit proves StepFor and Step drive the same kind state")
}

func TestKindStepFailsLoudWhenRegisteredWithoutImplementation(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	// 进程内注册表只加不减：登记一枚本测试二进制私有的词面（不与三种数据服务类型
	// 常量撞名，其余测试不枚举 Kinds()）。
	const orphan = "orphan-kind-under-test"
	dataservicetemplate.RegisterKind(dataservicetemplate.KindDescriptor{Kind: orphan, Quota: 1, EventPrefix: "orphan_kind_under_test"})
	assert.Panics(t, func() { fx.d.Step(context.Background(), orphan) },
		"a registered kind without a wired convergence step is an assembly gap: panic reaches loop.go and marks the ring dead (health-visible)")
	assert.Empty(t, fx.rt.calls())
}
