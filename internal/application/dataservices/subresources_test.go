package dataservices

// 子资源对账段单测（B8，ADR-0096 决策 10 / design §4.7）：kafka Topic 的
// ensure 矩阵（创建/分区增/分区减拒〔行落 error〕/retention ensure/ensure cache
// 幂等〔同签名窗内零动作〕）与声明式 GC（tombstone → 删实况 → 硬删行）。
// 实况由 fakeUtility 的可编程 stdout 承载（describe 首行——live 定案形）。

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/data/topic"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

// setTopicProbe 覆写 kafka describe 探测应答（out 空 = 缺主题：退出错 +
// 「does not exist」stderr——live 定案的缺席判据形）。
func setTopicProbe(t *testing.T, fx *fixture, out string) {
	t.Helper()
	fx.ut.mu.Lock()
	defer fx.ut.mu.Unlock()
	if fx.ut.probeScript == nil {
		fx.ut.probeScript = map[string]probeReply{}
	}
	if out == "" {
		fx.ut.probeScript["topic-inspect-"] = probeReply{
			stderr: "Error while executing topic command : Topic orders does not exist.",
			err:    errProbeExit,
		}
		return
	}
	fx.ut.probeScript["topic-inspect-"] = probeReply{stdout: out}
}

// errProbeExit 是探测应答脚本的退出错占位（fakeUtility 的 err 面）。
var errProbeExit = errors.New("probe exit 1")

// kafkaDescribeLine 铸 kafka describe 首行（live 定案形：Tab 分隔；
// retentionMs 0 = Configs 无该键 = 引擎缺省）。
func kafkaDescribeLine(partitions int32, retentionMs int64) string {
	configs := "min.insync.replicas=1"
	if retentionMs > 0 {
		configs += ",retention.ms=" + itoa64(retentionMs)
	}
	return "Topic: orders\tTopicId: x\tPartitionCount: " + itoa64(int64(partitions)) +
		"\tReplicationFactor: 1\tConfigs: " + configs + "\n"
}

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

// createTopicRow 在 kafka 夹具上落一行 Topic（不驱环）。
func createTopicRow(t *testing.T, fx *fixture, name string, partitions int32, retentionMS *int64) *topic.Topic {
	t.Helper()
	row := &topic.Topic{ProjectID: tProjectID, PubSubID: tKafkaID, Name: name, Partitions: partitions, RetentionMS: retentionMS}
	require.NoError(t, fx.stores.Topics.Create(context.Background(), row))
	return row
}

// topicGCIDs 报 topic-gc- 前缀（删除命令面）的调用 ID 集。
func topicGCIDs(fx *fixture) []string {
	var out []string
	for _, r := range fx.ut.requests() {
		if strings.HasPrefix(r.ID, "topic-gc-") {
			out = append(out, r.ID)
		}
	}
	return out
}

// topicUtilityIDs 报 topic- 前缀工具容器调用的 ID 集。
func topicUtilityIDs(fx *fixture) []string {
	var out []string
	for _, r := range fx.ut.requests() {
		if strings.HasPrefix(r.ID, "topic-") {
			out = append(out, r.ID)
		}
	}
	return out
}

// clearSubBackoff 清子资源对账失败退避窗（测试面：两段式用例跨窗续跑）。
func clearSubBackoff(fx *fixture) {
	fx.d.replayMu.Lock()
	defer fx.d.replayMu.Unlock()
	delete(fx.d.replayFails, "subresource-"+tKafkaID)
}

// TestTopicEnsureCreate 路径①：缺席 → create → 复核匹配 → ready。
func TestTopicEnsureCreate(t *testing.T) {
	fx := kafkaPubSubFixture(t, nil, nil)
	ctx := context.Background()
	require.NoError(t, fx.stores.PubSubs.SetStatus(ctx, tKafkaID, pubsub.StatusRunning))
	retention := int64(3600000)
	row := createTopicRow(t, fx, "orders", 3, &retention)

	setTopicProbe(t, fx, "") // 缺席：describe 退出错 → create 路径
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	require.Contains(t, topicUtilityIDs(fx), "topic-create-"+row.ID, "absent live topic renders a create command")
	// create 后复核仍缺席（stdout 空）→ 行落 error 如实（下一拍重试）。
	got, err := fx.stores.Topics.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, topic.StatusError, got.Status)
	assert.NotEmpty(t, got.StatusError)

	// 实况到场（describe 匹配期望）→ ready + 零变更命令（清退避窗——上一
	// 拍失败是脚本态，非被测语义）。
	setTopicProbe(t, fx, kafkaDescribeLine(3, 3600000))
	clearSubBackoff(fx)
	settled := len(topicUtilityIDs(fx))
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	got, err = fx.stores.Topics.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, topic.StatusReady, got.Status)
	assert.Empty(t, got.StatusError)
	for _, id := range topicUtilityIDs(fx)[settled:] {
		assert.NotContains(t, id, "topic-create-", "a matching live topic renders no create")
		assert.NotContains(t, id, "topic-partitions-", "a matching live topic renders no partition change")
		assert.NotContains(t, id, "topic-retention-", "a matching live topic renders no retention change")
	}
}

// TestTopicEnsureMemoIdempotence：同签名窗内零动作（ensure cache 命中——收敛幂等）。
func TestTopicEnsureMemoIdempotence(t *testing.T) {
	fx := kafkaPubSubFixture(t, nil, nil)
	ctx := context.Background()
	require.NoError(t, fx.stores.PubSubs.SetStatus(ctx, tKafkaID, pubsub.StatusRunning))
	row := createTopicRow(t, fx, "orders", 3, nil)
	setTopicProbe(t, fx, kafkaDescribeLine(3, 0))

	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	got, _ := fx.stores.Topics.Get(ctx, row.ID)
	require.Equal(t, topic.StatusReady, got.Status)
	before := len(topicUtilityIDs(fx))

	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	assert.Len(t, topicUtilityIDs(fx), before, "a fresh subMemo signature renders zero commands within the window")
}

// TestTopicEnsureDrift：漂移收敛（分区 3→5 + retention 缺席→设置）——每拍
// 强制重放窗内行集变化即失配（UpdateDesired 落行改签名）。
func TestTopicEnsureDrift(t *testing.T) {
	fx := kafkaPubSubFixture(t, nil, nil)
	ctx := context.Background()
	require.NoError(t, fx.stores.PubSubs.SetStatus(ctx, tKafkaID, pubsub.StatusRunning))
	row := createTopicRow(t, fx, "orders", 3, nil)
	setTopicProbe(t, fx, kafkaDescribeLine(3, 0))
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)

	retention := int64(7200000)
	require.NoError(t, fx.stores.Topics.UpdateDesired(ctx, row.ID, 5, &retention))
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	ids := topicUtilityIDs(fx)
	assert.Contains(t, ids, "topic-partitions-"+row.ID, "partition drift renders an alter")
	assert.Contains(t, ids, "topic-retention-"+row.ID, "retention drift renders a config alter")
}

// TestTopicEnsureShrinkRefused：实况分区超出期望（引擎不可减）→ 行落 error
// 如实（不可收敛漂移——建议行值上调或重建）。
func TestTopicEnsureShrinkRefused(t *testing.T) {
	fx := kafkaPubSubFixture(t, nil, nil)
	ctx := context.Background()
	require.NoError(t, fx.stores.PubSubs.SetStatus(ctx, tKafkaID, pubsub.StatusRunning))
	row := createTopicRow(t, fx, "orders", 2, nil)
	setTopicProbe(t, fx, kafkaDescribeLine(5, 0))
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	got, err := fx.stores.Topics.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, topic.StatusError, got.Status)
	assert.Contains(t, got.StatusError, "only increase")
	for _, id := range topicUtilityIDs(fx) {
		assert.NotContains(t, id, "topic-partitions-", "a shrink renders no partition command")
	}
}

// TestTopicDeclarativeGC：tombstone 行 → 删实况命令 → 行硬删（GC 收口）；
// 实况已缺席 → 直接硬删零命令。
func TestTopicDeclarativeGC(t *testing.T) {
	fx := kafkaPubSubFixture(t, nil, nil)
	ctx := context.Background()
	require.NoError(t, fx.stores.PubSubs.SetStatus(ctx, tKafkaID, pubsub.StatusRunning))
	row := createTopicRow(t, fx, "orders", 3, nil)
	setTopicProbe(t, fx, kafkaDescribeLine(3, 0))
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)

	require.NoError(t, fx.stores.Topics.SoftDelete(ctx, row.ID))
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	assert.Contains(t, topicUtilityIDs(fx), "topic-gc-"+row.ID, "a tombstoned row renders the delete command")
	rows, err := fx.stores.Topics.ListByPubSub(ctx, tKafkaID)
	require.NoError(t, err)
	assert.Empty(t, rows, "the row is hard-deleted after the live GC command succeeds")

	// 再删一行的 GC：实况缺席（describe 退出错）→ 只读探测后零删除命令
	// 直接硬删。
	row2 := createTopicRow(t, fx, "bills", 1, nil)
	setTopicProbe(t, fx, "")
	require.NoError(t, fx.stores.Topics.SoftDelete(ctx, row2.ID))
	before := len(topicGCIDs(fx))
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	assert.Len(t, topicGCIDs(fx), before, "an already-absent live topic renders zero delete commands (probe-only)")
	rows, _ = fx.stores.Topics.ListByPubSub(ctx, tKafkaID)
	assert.Empty(t, rows)
}

// TestSubResourceStatusGate：父实例非 running → 整段跳过（design §4.7 收敛
// 段前置 status 门——失败退避：实例不 running 跳过）。
func TestSubResourceStatusGate(t *testing.T) {
	fx := kafkaPubSubFixture(t, nil, nil)
	ctx := context.Background()
	// 夹具收敛后推进到 running 再显式回 pending（门判据是行面 status）。
	require.NoError(t, fx.stores.PubSubs.SetStatus(ctx, tKafkaID, pubsub.StatusRunning))
	row := createTopicRow(t, fx, "orders", 3, nil)
	require.NoError(t, fx.stores.PubSubs.SetStatus(ctx, tKafkaID, pubsub.StatusPending))
	before := len(topicUtilityIDs(fx))
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	assert.Len(t, topicUtilityIDs(fx), before, "a non-running parent skips the subresource segment entirely")
	got, err := fx.stores.Topics.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, topic.StatusPending, got.Status, "the row is untouched while the parent is not running")
}
