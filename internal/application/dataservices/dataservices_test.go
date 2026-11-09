package dataservices

// 分族步白盒（ADR-0096 决策 1 环表三条目 / B2e 验收锚）：①注册表缺席的
// 家族（pubsub/bucketstore 在 B4b/B5 登记前）调 Step 恒 no-op——占位环零
// 载体写；②StepFor(FamilyDatabase) 与 Step(ctx, FamilyDatabase) 同路径
//（环表挂载形态 = 分族步）；③在册而无收敛实现的家族 = panic（装配缺口
// fail-loud——loop.go「panic → ring dead」教义原样承接，不在卫星层 recover）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

func TestFamilyStepNoOpForUnregisteredFamilies(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	ctx := context.Background()

	// B4b 起 pubsub、B5 起 bucketstore 族在册（模板包初始化期登记——B2e
	// 占位环即活）。在册族的空行集 Step 不触 Runtime（零行零 Ensure）。
	_, pubsubRegistered := dataservicetemplate.LookupFamily(dataservicetemplate.FamilyPubSub)
	_, bucketRegistered := dataservicetemplate.LookupFamily(dataservicetemplate.FamilyBucketStore)
	require.True(t, pubsubRegistered, "B4b: pubsub registers with pubsubtemplate")
	require.True(t, bucketRegistered, "B5: bucketstore registers with bucketstoretemplate")

	fx.d.Step(ctx, dataservicetemplate.FamilyPubSub)
	fx.d.Step(ctx, dataservicetemplate.FamilyBucketStore)
	fx.d.StepFor(dataservicetemplate.FamilyPubSub)(ctx)
	fx.d.Step(ctx, "never-a-family")
	assert.Empty(t, fx.rt.calls(), "placeholder rings and empty registered rings must not touch the Runtime")
	assert.Empty(t, fx.ut.reqs, "placeholder rings must not run utility containers either")
}

func TestStepForBindsTheFamilyStep(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	ctx := context.Background()

	step := fx.d.StepFor(dataservicetemplate.FamilyDatabase)
	step(ctx)
	calls := fx.rt.calls()
	require.Len(t, calls, 1, "the database ring step converges the row through the family step")
	assert.Equal(t, tDatabaseID, calls[0].Spec["postgres"].ID)

	// 同一分族路径：第二拍经 Step 直调命中 memo 短路（Ensure 不再发）——
	// StepFor 闭包与 Step(ctx, family) 共享族状态面，而非第二份收敛器。
	fx.d.Step(ctx, dataservicetemplate.FamilyDatabase)
	assert.Len(t, fx.rt.calls(), 1, "signature short-circuit proves StepFor and Step drive the same family state")
}

func TestFamilyStepFailsLoudWhenRegisteredWithoutImplementation(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	// 进程内注册表只加不减：登记一枚本测试二进制私有的词面（不与三家族
	// 常量撞名，其余测试不枚举 Families()）。
	const orphan = "orphan-family-under-test"
	dataservicetemplate.RegisterFamily(dataservicetemplate.FamilyDescriptor{Family: orphan, Quota: 1, EventFamily: "orphan_family_under_test"})
	assert.Panics(t, func() { fx.d.Step(context.Background(), orphan) },
		"a registered family without a wired convergence step is an assembly gap: panic reaches loop.go and marks the ring dead (health-visible)")
	assert.Empty(t, fx.rt.calls())
}
