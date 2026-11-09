package dataservices

// redpanda 引擎的 pubsub 类型单测（B6b，ADR-0096 决策 7）：收敛投影面
// （FLEETLY_ADVERTISE_KAFKA env + bash 监督链 + admin 探针门）、Update
// dynamic 车道（rpk cluster config set——全 dynamic 旋钮集，无 static 车道）、
// pubsub_refs 注入面（KafkaDialect 基座同形 _BOOTSTRAP/_MECHANISM）。断言
// 值锚 pubsubtemplate live 定案形态（pubsubtemplate_test.go 同源）。

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// redpanda 夹具常量（kafka 夹具同源形态；ID 26 位 ULID 形——卷实例标记
// 门的 entity id 闸）。
const (
	tRedpandaID  = "01jdrpnd000000000000000000"
	tRedpandaNm  = "rbus"
	redpandaPass = "r123456789012345678901234567890123456789012345678" // 48 字符过渲染闸
)

// redpandaMaterialString 铸 kafka 方言串（redpanda 嵌基座——方言串同形；
// G101 纪律：键值拆串拼装）。
func redpandaMaterialString(pubsubID string) string {
	return "bootstrap=pub-" + pubsubID + ":9092;" +
		"mechanism=SCRAM-SHA-256;user=fleetly;" + "password=" + redpandaPass
}

// redpandaPubSubFixture 在夹具上补一枚 redpanda pubsub 行并推到收敛。
func redpandaPubSubFixture(t *testing.T, settings map[string]string) *fixture {
	t.Helper()
	fx := newFixture(t, "postgres", "postgresql://fleetly:x@db-01jd0db0000000000000000000:5432/fleetly", true)
	secName := upsertPubSubSecret(t, fx, "01JD0SEC0000000000000000R1", contract.PubCredentialSecretName(tRedpandaNm), redpandaMaterialString(tRedpandaID))
	createPubSubRow(t, fx, tRedpandaID, tRedpandaNm, "redpanda", "26.2", secName, settings, nil)
	fx.d.Step(context.Background(), dataservicetemplate.KindPubSub)
	require.NotEmpty(t, fx.rt.calls(), "redpanda fixture pubsub must converge")
	return fx
}

// TestRedpandaProjectionFaces 钉 redpanda 收敛投影：Spec["redpanda"] 的
// env 集（唯一键 FLEETLY_ADVERTISE_KAFKA）、bash 监督命令链、admin 探针
// 门、材料面（完整 yaml + scram）。
func TestRedpandaProjectionFaces(t *testing.T) {
	fx := redpandaPubSubFixture(t, nil)
	require.NotEmpty(t, fx.rt.calls())
	w := fx.rt.calls()[0].Spec["redpanda"]
	require.NotNil(t, w, "the redpanda carrier projection must exist")
	assert.Equal(t, "redpanda", w.Process)
	assert.Equal(t, "redpandadata/redpanda:v26.2.6@sha256:8b1076e0d61bfceb66594dc01536427ce0b529f8ead35fdab9763ab6085891ce", w.Image)
	require.NotEmpty(t, w.Volumes)
	assert.Equal(t, "/var/lib/redpanda/data", w.Volumes[0].Target)
	require.NotEmpty(t, w.Addressing)
	assert.Equal(t, "pub-"+tRedpandaID, w.Addressing[0].Name)
	// env 唯一键 = 通告面（redpanda 配置主体住材料 yaml，无 KAFKA_* env 面）。
	require.Len(t, w.Env, 1)
	assert.Equal(t, "pub-"+tRedpandaID+":9092", w.Env["FLEETLY_ADVERTISE_KAFKA"])
	// 命令面 = bash 监督链（起服/铸用户/硬化/门/孤儿收尾）。
	require.Len(t, w.Command, 3)
	assert.Equal(t, "/bin/bash", w.Command[0])
	chain := w.Command[2]
	for _, anchor := range []string{
		"rpk redpanda start --config /tmp/fleetly-redpanda.yaml",
		"--advertise-kafka-addr \"$FLEETLY_ADVERTISE_KAFKA\"",
		"rpk acl user create fleetly",
		"admin_api_require_auth true",
	} {
		assert.Contains(t, chain, anchor)
	}
	// 探针 = admin 就绪 + 引导完成门（复合 bash 面）。
	require.NotNil(t, w.Healthcheck)
	probe := strings.Join(w.Healthcheck.Exec, " ")
	assert.Contains(t, probe, "/v1/status/ready")
	assert.Contains(t, probe, "test -f /tmp/fleetly-boot-done")
	// 材料面：完整 yaml + scram 裸密码随 Ensure 下发。
	last := fx.rt.calls()[len(fx.rt.calls())-1]
	conf, ok := last.Materials.SecretFiles["database-redpanda-config"]
	require.True(t, ok, "the redpanda carrier must receive the config material")
	assert.Contains(t, string(conf), "auto_create_topics_enabled: false")
	scram, ok := last.Materials.SecretFiles["database-redpanda-scram"]
	require.True(t, ok, "the redpanda carrier must receive the scram password material")
	assert.Equal(t, redpandaPass, string(scram))
}

// TestRedpandaUpdateDynamicLane 钉 Update dynamic 车道（旋钮集全 dynamic：
// rpk cluster config set 工具容器 + ensure cache 记账；无 static 车道可滚——
// staticSettings 恒空）。未变重放零扰动。
func TestRedpandaUpdateDynamicLane(t *testing.T) {
	fx := redpandaPubSubFixture(t, nil)
	ctx := context.Background()

	res, err := fx.d.UpdatePubSub(ctx, tRedpandaID, PubSubUpdateRequest{Settings: map[string]string{"log_retention_ms": "90d"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"log_retention_ms"}, res.DynamicApplied)
	assert.Empty(t, res.StaticRolled)
	applyArgv := lastPubsettingsArgv(fx)
	assert.Contains(t, applyArgv, "rpk cluster config set log_retention_ms 7776000000 --config /run/secrets/database-redpanda-client")
	assert.Contains(t, applyArgv, "/bin/bash -c")
	_, remembered := fx.appliedMemo[tRedpandaID]
	assert.True(t, remembered, "the applied-digest memo is written after a successful dynamic apply")

	// 未变重放：摘要命中零扰动。
	before := pubsettingsUtilityCount(fx)
	res, err = fx.d.UpdatePubSub(ctx, tRedpandaID, PubSubUpdateRequest{Settings: map[string]string{"log_retention_ms": "90d"}})
	require.NoError(t, err)
	assert.Empty(t, res.DynamicApplied, "an unchanged declaration replays as a no-op")
	assert.Equal(t, before, pubsettingsUtilityCount(fx), "no utility execution for an unchanged dynamic declaration")

	// 移除 dynamic 键 = 以注册缺省复位（168h → 604800000ms——cluster
	// config 属性不随行删除回退，与 kafka 类型同律）。
	res, err = fx.d.UpdatePubSub(ctx, tRedpandaID, PubSubUpdateRequest{Settings: map[string]string{}})
	require.NoError(t, err)
	assert.Equal(t, []string{"log_retention_ms"}, res.DynamicApplied, "removal renders the registered default reset")
	assert.Contains(t, lastPubsettingsArgv(fx), "rpk cluster config set log_retention_ms 604800000")
}

// TestRedpandaInjectEnv 钉注入面方言：redpanda 经 KafkaDialect 基座出
// _BOOTSTRAP/_MECHANISM（+HOST/PORT/USER/PASSWORD 共面）——与 kafka 同形，
// 客户端零改动切换（design §4.6 方言族机制的用户面兑现）。
func TestRedpandaInjectEnv(t *testing.T) {
	fx := redpandaPubSubFixture(t, nil)
	spec := &specv1.AppSpec{Processes: []*specv1.ProcessSpec{{
		Name: "worker", PubsubRefs: []string{tRedpandaNm},
	}}}
	ws := []capability.Workload{{ID: "w1", Process: "worker"}}
	require.NoError(t, fx.d.InjectPubSubEnv(context.Background(), spec, ws, tProjectID))
	env := ws[0].Env
	assert.Equal(t, "pub-"+tRedpandaID+":9092", env["FLEETLY_PUB_RBUS_BOOTSTRAP"])
	assert.Equal(t, "SCRAM-SHA-256", env["FLEETLY_PUB_RBUS_MECHANISM"])
	assert.Equal(t, "fleetly", env["FLEETLY_PUB_RBUS_USER"])
	assert.Equal(t, redpandaPass, env["FLEETLY_PUB_RBUS_PASSWORD"])
	assert.NotContains(t, env, "FLEETLY_PUB_RBUS_URL", "the kafka dialect carries no URL form")
}

// TestRedpandaNoBackupFacePlumbing 钉能力位判据（排程/受理门单源：
// EngineBacksUp false 挂账——kafka 同类型同裁；目录诚实推论）。
func TestRedpandaNoBackupFacePlumbing(t *testing.T) {
	if pubsubtemplate.EngineBacksUp("redpanda") {
		t.Fatalf("redpanda carries no data backup face (deferred) — schedulePass must never cast rows for it")
	}
	for _, e := range pubsubtemplate.Catalog() {
		if e.Engine == "redpanda" && (e.Capabilities.Backup || e.Capabilities.Migrate) {
			t.Fatalf("redpanda catalog must carry backup:false migrate:false, got %+v", e.Capabilities)
		}
	}
}
