package dataservices

// kafka 引擎的 pubsub 类型单测（B6a，ADR-0096 决策 6/7）：收敛投影面（KRaft
// env 集 + SCRAM 引导链 + 探针分面）、Update 双车道（dynamic 热应用 =
// kafka-configs.sh、static heap 滚动）、循环重放门、pubsub_refs 注入面
// （FLEETLY_PUB_*_BOOTSTRAP/MECHANISM）。断言值锚 pubsubtemplate live 定案
// 形态（pubsubtemplate_test.go 同源）。

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
)

// kafka 夹具常量（origin_test.go 的 pubsub 类型同源形态）。
const (
	tKafkaID  = "01jdkfk0000000000000000000"
	tKafkaNm  = "kbus"
	kafkaPass = "k123456789012345678901234567890123456789012345678" // 48 字符过渲染闸
)

// kafkaMaterialString 铸 kafka 方言串（G101 纪律：键值拆串拼装，源字面量
// 不含完整 password=value 形态）。
func kafkaMaterialString(pubsubID string) string {
	return "bootstrap=pub-" + pubsubID + ":9092;" +
		"mechanism=SCRAM-SHA-256;user=fleetly;" + "password=" + kafkaPass
}

// upsertPubSubSecret 落一枚 pubsub 凭证 Secret（方言串密封）。
func upsertPubSubSecret(t *testing.T, fx *fixture, id, name, materialValue string) string {
	t.Helper()
	ct, err := fx.cipher.Seal([]byte(materialValue))
	require.NoError(t, err)
	require.NoError(t, fx.stores.Secrets.Upsert(context.Background(), &secret.Secret{
		ID: id, ProjectID: tProjectID, Name: name,
		Ciphertext: ct, Fingerprint: material.Fingerprint([]byte(materialValue)),
	}))
	return name
}

// createPubSubRow 落一枚 pubsub 行。
func createPubSubRow(t *testing.T, fx *fixture, id, name, engine, version, secName string, settings map[string]string, memoryMB *int32) {
	t.Helper()
	require.NoError(t, fx.stores.PubSubs.Create(context.Background(), &pubsub.PubSub{
		ID: id, ProjectID: tProjectID, Name: name,
		Engine: engine, Version: version, CredentialsRef: secName,
		Settings: settings, MemoryMB: memoryMB,
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}))
}

// kafkaPubSubFixture 在夹具上补一枚 kafka pubsub 行并推到收敛。
func kafkaPubSubFixture(t *testing.T, settings map[string]string, memoryMB *int32) *fixture {
	t.Helper()
	fx := newFixture(t, "postgres", "postgresql://fleetly:x@db-01jd0db0000000000000000000:5432/fleetly", true)
	secName := upsertPubSubSecret(t, fx, "01JD0SEC0000000000000000K1", contract.PubCredentialSecretName(tKafkaNm), kafkaMaterialString(tKafkaID))
	createPubSubRow(t, fx, tKafkaID, tKafkaNm, "kafka", "4.4", secName, settings, memoryMB)
	fx.d.Step(context.Background(), dataservicetemplate.KindPubSub)
	require.NotEmpty(t, fx.rt.calls(), "kafka fixture pubsub must converge")
	return fx
}

// pubsettingsUtilityCount 报 pubsettings- 前缀（kafka dynamic 热应用面）
// 的工具容器调用数。
func pubsettingsUtilityCount(fx *fixture) int {
	n := 0
	for _, r := range fx.ut.requests() {
		if strings.HasPrefix(r.ID, "pubsettings-") {
			n++
		}
	}
	return n
}

// lastPubsettingsArgv 返回最近一次 pubsettings- 调用的 argv 串（无则空）。
func lastPubsettingsArgv(fx *fixture) string {
	out := ""
	for _, r := range fx.ut.requests() {
		if strings.HasPrefix(r.ID, "pubsettings-") {
			out = strings.Join(r.Argv, " ")
		}
	}
	return out
}

// TestKafkaProjectionFaces 钉 kafka 收敛投影：Spec["kafka"] 的 env 集
// （KRaft/SASL/heap 联动——内存 1024 → heap 512m）与 SCRAM 引导命令面、
// 探针分面（PROBE listener）。
func TestKafkaProjectionFaces(t *testing.T) {
	mem := int32(1024)
	fx := kafkaPubSubFixture(t, map[string]string{"num.partitions": "4"}, &mem)
	require.NotEmpty(t, fx.rt.calls())
	w := fx.rt.calls()[0].Spec["kafka"]
	require.NotNil(t, w, "the kafka carrier projection must exist")
	assert.Equal(t, "kafka", w.Process)
	assert.Equal(t, "apache/kafka:4.4.0@sha256:833b867a9585a141da455dc2b57431c65cbccb27f11c91f694444cca9f777e0f", w.Image)
	require.NotEmpty(t, w.Volumes)
	assert.Equal(t, "/var/lib/kafka/data", w.Volumes[0].Target)
	require.NotEmpty(t, w.Addressing)
	assert.Equal(t, "pub-"+tKafkaID, w.Addressing[0].Name)
	for key, want := range map[string]string{
		"KAFKA_PROCESS_ROLES":                        "broker,controller",
		"KAFKA_ADVERTISED_LISTENERS":                 "BROKER://pub-" + tKafkaID + ":9092,PROBE://127.0.0.1:9094",
		"KAFKA_SASL_ENABLED_MECHANISMS":              "SCRAM-SHA-256",
		"KAFKA_SASL_MECHANISM_INTER_BROKER_PROTOCOL": "SCRAM-SHA-256",
		"KAFKA_HEAP_OPTS":                            "-Xms512m -Xmx512m", // F-13 分支二：memory 1024 → heap = 0.5×
		"KAFKA_NUM_PARTITIONS":                       "4",
	} {
		assert.Equal(t, want, w.Env[key], "env %s", key)
	}
	require.Len(t, w.Command, 3)
	assert.Equal(t, "/bin/bash", w.Command[0])
	assert.Contains(t, w.Command[2], "kafka-storage.sh format --ignore-formatted")
	assert.Contains(t, w.Command[2], "--add-scram")
	// 探针 = 内部明文 listener 分面（PROBE 9094）。
	require.NotNil(t, w.Healthcheck)
	assert.Equal(t, "/opt/kafka/bin/kafka-broker-api-versions.sh", w.Healthcheck.Exec[0])
	assert.Contains(t, strings.Join(w.Healthcheck.Exec, " "), "localhost:9094")
	// 材料面：JAAS（KafkaServer 上下文）随 Ensure 下发。
	last := fx.rt.calls()[len(fx.rt.calls())-1]
	jaas, ok := last.Materials.SecretFiles["database-kafka-jaas"]
	require.True(t, ok, "the kafka carrier must receive the JAAS material")
	assert.Contains(t, string(jaas), `username="fleetly"`)
	scram, ok := last.Materials.SecretFiles["database-kafka-scram"]
	require.True(t, ok, "the kafka carrier must receive the SCRAM bootstrap password")
	assert.Equal(t, kafkaPass, string(scram))
}

// TestKafkaUpdateDualLane 钉 Update 双车道：dynamic（log.retention.ms）→
// 同步热应用先行（pubsettings- 工具容器 + ensure cache 记账 + 行落库）；static
// （heap_mb）→ 落行 + Kick 滚动（env 面含新 heap）；未变重放零扰动。
func TestKafkaUpdateDualLane(t *testing.T) {
	fx := kafkaPubSubFixture(t, nil, nil)
	ctx := context.Background()

	// dynamic 车道。
	res, err := fx.d.UpdatePubSub(ctx, tKafkaID, PubSubUpdateRequest{Settings: map[string]string{"log.retention.ms": "90d"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"log.retention.ms"}, res.DynamicApplied)
	assert.Empty(t, res.StaticRolled)
	applyArgv := lastPubsettingsArgv(fx)
	assert.Contains(t, applyArgv, "kafka-configs.sh --bootstrap-server pub-"+tKafkaID+":9092")
	assert.Contains(t, applyArgv, "--entity-type brokers --entity-name 1")
	assert.Contains(t, applyArgv, "log.retention.ms=7776000000", "duration knob renders as native milliseconds")
	_, remembered := fx.appliedMemo[tKafkaID]
	assert.True(t, remembered, "the applied-digest memo is written after a successful dynamic apply")

	// 未变重放：摘要命中零扰动（无新工具容器调用）。
	before := pubsettingsUtilityCount(fx)
	res, err = fx.d.UpdatePubSub(ctx, tKafkaID, PubSubUpdateRequest{Settings: map[string]string{"log.retention.ms": "90d"}})
	require.NoError(t, err)
	assert.Empty(t, res.DynamicApplied, "an unchanged declaration replays as a no-op")
	assert.Equal(t, before, pubsettingsUtilityCount(fx), "no utility execution for an unchanged dynamic declaration")

	// static 车道（heap_mb）：落行 + Kick + 下一拍 env 含新 heap。
	res, err = fx.d.UpdatePubSub(ctx, tKafkaID, PubSubUpdateRequest{Settings: map[string]string{
		"log.retention.ms": "90d", "heap_mb": "384mb",
	}})
	require.NoError(t, err)
	assert.Equal(t, []string{"heap_mb"}, res.StaticRolled)
	assert.GreaterOrEqual(t, fx.pubKicks, 1, "static lane kicks the pubsub ring")
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	w := fx.rt.calls()[len(fx.rt.calls())-1].Spec["kafka"]
	require.NotNil(t, w)
	assert.Equal(t, "-Xms384m -Xmx384m", w.Env["KAFKA_HEAP_OPTS"], "static env knob rides the carrier spec (rolling)")
	// 喂入律：dynamic 键永不进 Workload env。
	assert.NotContains(t, w.Env, "KAFKA_LOG_RETENTION_MS")
}

// TestKafkaUpdateDynamicRemovalResets 移除 dynamic 键 = 以注册缺省复位
// （168h → 604800000ms——KRaft 动态配置不随行删除回退）。
func TestKafkaUpdateDynamicRemovalResets(t *testing.T) {
	fx := kafkaPubSubFixture(t, map[string]string{"log.retention.ms": "30d"}, nil)
	res, err := fx.d.UpdatePubSub(context.Background(), tKafkaID, PubSubUpdateRequest{Settings: map[string]string{}})
	require.NoError(t, err)
	assert.Equal(t, []string{"log.retention.ms"}, res.DynamicApplied, "removal renders the registered default reset")
	assert.Contains(t, lastPubsettingsArgv(fx), "log.retention.ms=604800000")
}

// TestKafkaReplayGate 钉循环重放门：ensure cache 失配（进程重启 = ensure cache 空）→ Step
// 重放 dynamic 子集一次；ensure cache 回填后零扰动。
func TestKafkaReplayGate(t *testing.T) {
	fx := kafkaPubSubFixture(t, nil, nil)
	ctx := context.Background()
	// 重放门②：仅 running 行重放（红队 A7——观测驱动的状态面在夹具里显式落）。
	require.NoError(t, fx.stores.PubSubs.SetStatus(ctx, tKafkaID, pubsub.StatusRunning))
	_, err := fx.d.UpdatePubSub(ctx, tKafkaID, PubSubUpdateRequest{Settings: map[string]string{"message.max.bytes": "2mb"}})
	require.NoError(t, err)
	// 模拟进程重启：ensure cache 表清空 → 下一拍循环重放。
	for k := range fx.appliedMemo {
		delete(fx.appliedMemo, k)
	}
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	require.Equal(t, 1, pubsettingsUtilityCount(fx)-1, "a lost memo triggers exactly one idempotent replay (beyond the Update apply itself)")
	// 重放成功后 ensure cache 回填 → 再拍零扰动。
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	assert.Equal(t, 2, pubsettingsUtilityCount(fx), "a hit memo must not replay again")
}

// TestKafkaInjectEnv 钉注入面方言：kafka 出 _BOOTSTRAP/_MECHANISM（+
// HOST/PORT/USER/PASSWORD 共面），无 _URL；nats 对照（B4b URL 契约零回归）。
func TestKafkaInjectEnv(t *testing.T) {
	fx := kafkaPubSubFixture(t, nil, nil)
	ctx := context.Background()
	spec := &specv1.AppSpec{Processes: []*specv1.ProcessSpec{{
		Name: "worker", PubsubRefs: []string{tKafkaNm},
	}}}
	ws := []capability.Workload{{ID: "w1", Process: "worker"}}
	require.NoError(t, fx.d.InjectPubSubEnv(ctx, spec, ws, tProjectID))
	env := ws[0].Env
	assert.Equal(t, "pub-"+tKafkaID+":9092", env["FLEETLY_PUB_KBUS_BOOTSTRAP"])
	assert.Equal(t, "SCRAM-SHA-256", env["FLEETLY_PUB_KBUS_MECHANISM"])
	assert.Equal(t, "pub-"+tKafkaID, env["FLEETLY_PUB_KBUS_HOST"])
	assert.Equal(t, "9092", env["FLEETLY_PUB_KBUS_PORT"])
	assert.Equal(t, "fleetly", env["FLEETLY_PUB_KBUS_USER"])
	assert.Equal(t, kafkaPass, env["FLEETLY_PUB_KBUS_PASSWORD"])
	assert.NotContains(t, env, "FLEETLY_PUB_KBUS_URL", "the kafka dialect carries no URL form")

	// nats 方言对照。
	const natsID = "01jdnats0000000000000000000"
	natsMaterial := "nats://fleetly:" + strings.Repeat("n", 48) + "@pub-" + natsID + ":4222"
	secName := upsertPubSubSecret(t, fx, "01JD0SEC0000000000000000N1", contract.PubCredentialSecretName("bus"), natsMaterial)
	createPubSubRow(t, fx, natsID, "bus", "nats", "2.11", secName, nil, nil)
	spec2 := &specv1.AppSpec{Processes: []*specv1.ProcessSpec{{Name: "worker", PubsubRefs: []string{"bus"}}}}
	ws2 := []capability.Workload{{ID: "w2", Process: "worker"}}
	require.NoError(t, fx.d.InjectPubSubEnv(ctx, spec2, ws2, tProjectID))
	assert.Equal(t, natsMaterial, ws2[0].Env["FLEETLY_PUB_BUS_URL"])
	assert.NotContains(t, ws2[0].Env, "FLEETLY_PUB_BUS_BOOTSTRAP")
}

// TestKafkaNoBackupFacePlumbing 钉能力位判据源（排程/受理门消费的
// EngineBacksUp 单源——kafka false 挂账 / nats true；诚实推论 migrate）。
func TestKafkaNoBackupFacePlumbing(t *testing.T) {
	if pubsubtemplate.EngineBacksUp("kafka") {
		t.Fatalf("kafka carries no data backup face (deferred) — schedulePass must never cast rows for it")
	}
	if !pubsubtemplate.EngineBacksUp("nats") {
		t.Fatalf("nats carries the per-stream backup face")
	}
	for _, e := range pubsubtemplate.Catalog() {
		if e.Engine == "kafka" && (e.Capabilities.Backup || e.Capabilities.Migrate) {
			t.Fatalf("kafka catalog must carry backup:false migrate:false, got %+v", e.Capabilities)
		}
	}
}
