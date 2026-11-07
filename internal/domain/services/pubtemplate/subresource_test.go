package pubtemplate

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 子资源方言面钉测（B8，ADR-0096 决策 10 / design §4.7）：解析器以 2026-11-07
// live 实测原文为夹具（kafka 4.4.0 describe 首行 / redpanda v26.2.6 describe
// 表 / nats-box 0.15.0 stream info JSON），argv 形态钉方言分派与继承律
// （TopicAdminFace 两实现各自完整——kafka 目标值 vs redpanda 增量）。

const liveKafkaDescribe = "Topic: probe\tTopicId: byUbmdtkQAaXuoR36ubqtg\tPartitionCount: 3\tReplicationFactor: 1\tConfigs: min.insync.replicas=1,retention.ms=3600000\n" +
	"\tTopic: probe\tPartition: 0\tLeader: 1\tReplicas: 1\tIsr: 1\tElr: \tLastKnownElr: \n"

const liveRedpandaDescribe = `SUMMARY
=======
NAME        probe
PARTITIONS  3
REPLICAS    1

CONFIGS
=======
KEY                                   VALUE          SOURCE
cleanup.policy                        delete         DEFAULT_CONFIG
retention.bytes                       -1             DEFAULT_CONFIG
retention.ms                          3600000        DYNAMIC_TOPIC_CONFIG
segment.bytes                         134217728      DEFAULT_CONFIG
`

const liveNatsStreamInfo = `{
  "config": {
    "name": "ORDERS",
    "subjects": ["orders.>"],
    "retention": "limits",
    "max_bytes": 1048576,
    "storage": "file",
    "num_replicas": 1
  },
  "state": {"messages": 3, "bytes": 1234}
}`

func kafkaFace(t *testing.T) TopicAdminFace {
	t.Helper()
	tpl, err := For("kafka", "")
	require.NoError(t, err)
	face := TopicAdminFaceOf(tpl)
	require.NotNil(t, face, "kafka carries the topic admin face")
	return face
}

func redpandaFace(t *testing.T) TopicAdminFace {
	t.Helper()
	tpl, err := For("redpanda", "")
	require.NoError(t, err)
	face := TopicAdminFaceOf(tpl)
	require.NotNil(t, face, "redpanda carries the topic admin face (its own implementation)")
	return face
}

func TestSubResourceFaceAvailabilityByEngine(t *testing.T) {
	nats, err := For("nats", "")
	require.NoError(t, err)
	assert.Nil(t, TopicAdminFaceOf(nats), "nats carries no topic face")
	assert.NotNil(t, StreamAdminFaceOf(nats), "nats carries the stream face")
	kafka, err := For("kafka", "")
	require.NoError(t, err)
	assert.Nil(t, StreamAdminFaceOf(kafka), "kafka carries no stream face")
}

func TestKafkaTopicInspectParse(t *testing.T) {
	face := kafkaFace(t)
	live, err := face.ParseTopicInspect(liveKafkaDescribe, "", nil)
	require.NoError(t, err)
	assert.True(t, live.Exists)
	assert.Equal(t, int32(3), live.Partitions)
	require.NotNil(t, live.RetentionMS)
	assert.Equal(t, int64(3600000), *live.RetentionMS)

	// Configs 无 retention.ms = 引擎缺省（不管理）。
	live, err = face.ParseTopicInspect("Topic: a\tTopicId: x\tPartitionCount: 4\tReplicationFactor: 1\tConfigs: min.insync.replicas=1\n", "", nil)
	require.NoError(t, err)
	assert.Nil(t, live.RetentionMS)

	// 缺主题 = 退出错 + "does not exist"（live 文案）。
	live, err = face.ParseTopicInspect("", "Error while executing topic command : Topic 'missing' does not exist as expected", errors.New("exit 1"))
	require.NoError(t, err)
	assert.False(t, live.Exists)

	// 其余退出错 = 执行错误，带 stderr 末行实义（kafka 异常栈尾行回退一行）。
	_, err = face.ParseTopicInspect("", "Error: SASL authentication failed\n (org.apache.kafka.tools.TopicCommand)", errors.New("exit 1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SASL authentication failed")
}

func TestRedpandaTopicInspectParse(t *testing.T) {
	face := redpandaFace(t)
	live, err := face.ParseTopicInspect(liveRedpandaDescribe, "", nil)
	require.NoError(t, err)
	assert.True(t, live.Exists)
	assert.Equal(t, int32(3), live.Partitions)
	require.NotNil(t, live.RetentionMS)
	assert.Equal(t, int64(3600000), *live.RetentionMS)

	// 缺主题形态一（B8 记录）：退出 0 + 空 SUMMARY。
	live, err = face.ParseTopicInspect("SUMMARY\n=======\n", "", nil)
	require.NoError(t, err)
	assert.False(t, live.Exists)

	// 缺主题形态二（B8-fix live 复核，v26.2.6 profile 材料面实录）：退出 1
	// + stderr UNKNOWN_TOPIC_OR_PARTITION + stdout 仍带 SUMMARY（NAME 行 +
	// PARTITIONS 0）——PARTITIONS 0 行是缺席判据（存在主题恒 ≥1 分区）。
	live, err = face.ParseTopicInspect(
		"SUMMARY\n=======\nNAME        rp-orders\nPARTITIONS  0\n",
		"config response contained error: UNKNOWN_TOPIC_OR_PARTITION: This server does not host this topic-partition.",
		errors.New("exit 1"))
	require.NoError(t, err)
	assert.False(t, live.Exists, "exit 1 with a zero-partitions SUMMARY row is the absent form, not a probe error")

	// 非缺席的退出错（无 SUMMARY 面）仍如实报错——认证/网络故障不是缺席。
	_, err = face.ParseTopicInspect("", "Error: SASL authentication failed", errors.New("exit 1"))
	require.Error(t, err, "an exit failure without the SUMMARY face is a real probe error")
}

func TestTopicArgvDialects(t *testing.T) {
	retention := int64(7200000)
	k, err := kafkaFace(t).CreateTopic("pub-x", "pw0123456789", "orders", 3, &retention)
	require.NoError(t, err)
	argv := strings.Join(k.Argv, " ")
	assert.Contains(t, argv, "/opt/kafka/bin/kafka-topics.sh")
	assert.Contains(t, argv, "--replication-factor 1", "replicas are fixed at 1 on the single broker")
	assert.Contains(t, argv, "--config retention.ms=7200000")
	assert.Contains(t, k.SecretFiles, kafkaClientPropsFile, "SASL credentials ride the client-properties material, never argv")
	assert.NotContains(t, argv, "pw0123456789")

	ka, err := kafkaFace(t).AlterTopicPartitions("pub-x", "pw0123456789", "orders", 3, 5)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(ka.Argv, " "), "--partitions 5", "kafka --partitions is the target count")

	kr, err := kafkaFace(t).AlterTopicRetention("pub-x", "pw0123456789", "orders", 60000)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(kr.Argv, " "), "kafka-configs.sh")
	assert.Contains(t, strings.Join(kr.Argv, " "), "--entity-type topics --entity-name orders --add-config retention.ms=60000")

	ra, err := redpandaFace(t).AlterTopicPartitions("pub-x", "pw0123456789", "orders", 3, 5)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(ra.Argv, " "), "add-partitions orders -n 2", "rpk add-partitions -n is the delta (live: 3 + 5 = 8)")
	assert.Contains(t, ra.SecretFiles, redpandaClientFile)
	_, err = redpandaFace(t).AlterTopicPartitions("pub-x", "pw0123456789", "orders", 5, 5)
	require.Error(t, err, "a non-positive delta is refused")

	rd, err := redpandaFace(t).DeleteTopic("pub-x", "pw0123456789", "orders")
	require.NoError(t, err)
	assert.Equal(t, []string{"rpk", "topic", "delete", "orders"}, rd.Argv[:4])
}

func TestTopicPartitionsIncreaseOnly(t *testing.T) {
	assert.NoError(t, kafkaFace(t).ValidateTopicPartitionsChange(3, 3))
	assert.NoError(t, kafkaFace(t).ValidateTopicPartitionsChange(3, 6))
	assert.Error(t, kafkaFace(t).ValidateTopicPartitionsChange(6, 3))
	assert.Error(t, redpandaFace(t).ValidateTopicPartitionsChange(6, 3))
}

func TestValidateTopicName(t *testing.T) {
	assert.NoError(t, ValidateTopicName("orders.v1_main-a"))
	assert.Error(t, ValidateTopicName(""))
	assert.Error(t, ValidateTopicName("bad name"))
	assert.Error(t, ValidateTopicName("a/b"))
}

func TestNatsStreamFace(t *testing.T) {
	tpl, err := For("nats", "")
	require.NoError(t, err)
	face := StreamAdminFaceOf(tpl)

	live, err := face.ParseStreamInspect(liveNatsStreamInfo, "", nil)
	require.NoError(t, err)
	assert.True(t, live.Exists)
	assert.Equal(t, []string{"orders.>"}, live.Subjects)
	assert.Equal(t, "limits", live.Retention)
	assert.Equal(t, "file", live.Storage)
	assert.True(t, live.MaxBytesSet)
	assert.Equal(t, int64(1048576), live.MaxBytes)
	assert.Equal(t, int64(3), live.Messages)
	assert.Equal(t, int64(1234), live.Bytes)

	// 缺名流：nats-box 0.15.0 报 "could not pick a Stream"（live 原文）→ 缺席。
	live, err = face.ParseStreamInspect("", "nats: error: could not pick a Stream to operate on: cannot pick a Stream without a terminal and no Stream name supplied", errors.New("exit 1"))
	require.NoError(t, err)
	assert.False(t, live.Exists)

	max := int64(2048)
	add, err := face.CreateStream("pub-x", "pw0123456789", "ORDERS", []string{"orders.>", "bills.>"}, "limits", "file", &max)
	require.NoError(t, err)
	argv := strings.Join(add.Argv, " ")
	assert.Contains(t, argv, "nats stream add ORDERS")
	assert.Contains(t, argv, "--defaults", "non-interactive add requires --defaults (live)")
	assert.Contains(t, argv, "--replicas 1")
	assert.Contains(t, argv, "--subjects orders.>,bills.>")
	assert.Contains(t, argv, "--max-bytes 2048")
	assert.Equal(t, "pw0123456789", add.Env["NATS_PASSWORD"], "nats-box credentials ride the registered env exception")

	upd, err := face.UpdateStream("pub-x", "pw0123456789", "ORDERS", []string{"orders.>"}, nil)
	require.NoError(t, err)
	uargv := strings.Join(upd.Argv, " ")
	assert.Contains(t, uargv, "nats stream update ORDERS -f")
	assert.NotContains(t, uargv, "--retention", "retention is immutable after create (no update flag, live)")
	assert.NotContains(t, uargv, "--storage")

	rm, err := face.DeleteStream("pub-x", "pw0123456789", "ORDERS")
	require.NoError(t, err)
	assert.Equal(t, []string{"nats", "stream", "rm", "ORDERS", "-f"}, rm.Argv)
}
