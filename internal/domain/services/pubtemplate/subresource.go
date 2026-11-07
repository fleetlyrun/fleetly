package pubtemplate

// 子资源管理方言面（B8，ADR-0096 决策 10 / design §4.7）：Topic（kafka 协议
// 族）与 Stream（nats）的 admin 工具容器命令渲染 + 实况解析。形态 = 声明行
// + admin 工具容器收敛：dataservices 卫星分族步尾段的子资源对账段消费本面
// （对照行期望态 ensure——存在+配置匹配否则创建/更新；删除 = 声明式 GC）。
//
// 全部命令面 live 定案（2026-11-07 本机 docker 实测）：
//   - kafka（apache/kafka:4.4.0 自带 /opt/kafka/bin）：`kafka-topics.sh
//     --create/--alter --partitions/--delete`（alter 分区可增不可减——
//     InvalidPartitionsException）；retention 面 = `kafka-configs.sh --alter
//     --entity-type topics --add-config retention.ms=<ms>`；describe 首行
//     `Topic: NAME\tTopicId: …\tPartitionCount: N\tReplicationFactor: 1\t
//     Configs: …retention.ms=MS…`（未显式设置时 Configs 无该键 = 引擎缺省
//     ——retention 不管理即不对照）。缺省内部主题 __consumer_offsets 等
//     不在平台行面（负空间：平台只管声明行）。
//   - redpanda（redpandadata/redpanda v26.2.6 自带 rpk）：`rpk topic
//     create/describe/add-partitions/alter-config --set/delete --config
//     <profile>`；describe 输出 SUMMARY（NAME/PARTITIONS/REPLICAS）+ CONFIGS
//     表（KEY VALUE SOURCE——DYNAMIC_TOPIC_CONFIG = 显式设置）；缺主题时
//     describe 退出码 0 且 SUMMARY 空 = 缺席判据。
//   - nats（natsio/nats-box 0.15.0）：`nats stream add NAME --subjects …
//     --retention … --storage … --max-bytes N --replicas 1 --defaults`（非
//     交互需 --defaults）；**update 无 --retention/--storage 旗标 → 两轴
//     创建后不可变**（可变面 = --subjects/--max-bytes）；`nats stream info
//     NAME --json`（config.{subjects,retention,storage,max_bytes} +
//     state.{messages,bytes}）；`nats stream rm NAME -f`。
//
// 基座继承律（kafkadialect.go）在子资源面的映射：TopicAdminFace 不入基座
// ——kafka/redpanda 各自完整实现（命令载体与解析面双双分立，继承律的
// 「各自覆盖」半边结构性满足）；StreamAdminFace 是 nats 专属面。

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// AdminSpec 是一条 admin 工具容器命令（BackupSpec 的再导出别名——argv/
// env/材料三段同形；单命令单目的：ensure 对账段按差异逐条渲染。nats 的
// InspectStream 与 StreamDumpFace 既有探测面同命令复用，别名使签名互通）。
type AdminSpec = enginetemplate.BackupSpec

// TopicLive 是 Topic 实况的解析面（InspectTopic 产物）。
type TopicLive struct {
	// Exists 报告主题在场（缺席 = ensure 段创建路径）。
	Exists bool
	// Partitions 是实况分区数。
	Partitions int32
	// RetentionMS 是实况显式 retention.ms（nil = 未显式设置 = 引擎缺省）。
	RetentionMS *int64
}

// StreamLive 是 Stream 实况的解析面（含 state 面——删除披露行消费）。
type StreamLive struct {
	Exists    bool
	Subjects  []string
	Retention string
	Storage   string
	MaxBytes  int64
	// MaxBytesSet 区分「未设上限」(false) 与「上限 0」——nats max_bytes=-1
	// 表示未设。
	MaxBytesSet bool
	// Messages/Bytes 是流内消息面（state.bytes/messages——删除披露）。
	Messages int64
	Bytes    int64
}

// TopicAdminFace 是 Topic 的 admin 方言面（kafka 协议族引擎实现：
// kafkaTemplate/redpandaTemplate）。host 是父实例网内 DNS 名（pub-<id>）；
// 密码过渲染闸；凭证经材料文件（kafka = client.properties / redpanda =
// rpk profile），argv 不载凭证。
type TopicAdminFace interface {
	// InspectTopic 渲染实况探测（describe）。
	InspectTopic(host, password, topic string) (AdminSpec, error)
	// ParseTopicInspect 解析探测产物（stdout/stderr/执行错误三元组——
	// 方言各自的缺席判据在此收口：kafka = 退出错 + "does not exist"；redpanda
	// = 退出 0 + 空 SUMMARY）。
	ParseTopicInspect(stdout, stderr string, runErr error) (TopicLive, error)
	// CreateTopic 渲染创建（副本因子恒 1——单 broker 事实，目录 note 标注；
	// retentionMS nil = 不设 retention.ms）。
	CreateTopic(host, password, topic string, partitions int32, retentionMS *int64) (AdminSpec, error)
	// AlterTopicPartitions 渲染分区变更（from → to；方言各自折算载体：
	// kafka `--partitions <to>` 是目标值、redpanda `add-partitions -n` 是
	// 增量——live 定案。引擎约束可增不可减：ValidateTopicPartitionsChange
	// 是受理半边，alter 退出错是纵深半边）。
	AlterTopicPartitions(host, password, topic string, from, to int32) (AdminSpec, error)
	// AlterTopicRetention 渲染 retention 变更（kafka-configs.sh / rpk
	// alter-config 面）。
	AlterTopicRetention(host, password, topic string, retentionMS int64) (AdminSpec, error)
	// DeleteTopic 渲染删除（数据删除——wide 档确认后的 GC 执行面）。
	DeleteTopic(host, password, topic string) (AdminSpec, error)
	// ValidateTopicPartitionsChange 校验分区变更的引擎约束（受理位消费：
	// from → to 只增不减）。
	ValidateTopicPartitionsChange(from, to int32) error
}

// StreamAdminFace 是 Stream 的 admin 方言面（natsTemplate 实现）。凭证经
// NATS_URL/NATS_USER/NATS_PASSWORD env（nats-box 一次性 env 例外，
// ADR-0039 决策 5 例外族既有登记）。
type StreamAdminFace interface {
	// InspectStream 渲染实况探测（`nats stream info --json`）。
	InspectStream(host, password, stream string) (AdminSpec, error)
	// ParseStreamInspect 解析探测产物（缺席判据：nats-box 对缺名流的报错
	// 面——“not found”/无终端交互缺席类——退出错归缺席需模式识别，非该
	// 模式归执行错误）。
	ParseStreamInspect(stdout, stderr string, runErr error) (StreamLive, error)
	// CreateStream 渲染创建（--defaults 非交互；replicas 恒 1）。
	CreateStream(host, password, stream string, subjects []string, retention, storage string, maxBytes *int64) (AdminSpec, error)
	// UpdateStream 渲染可变面变更（--subjects/--max-bytes；retention/
	// storage 创建后不可变——CLI 无旗标，受理位拒）。
	UpdateStream(host, password, stream string, subjects []string, maxBytes *int64) (AdminSpec, error)
	// DeleteStream 渲染删除（数据删除——wide 档确认后的 GC 执行面）。
	DeleteStream(host, password, stream string) (AdminSpec, error)
}

// TopicAdminFaceOf 把绑版 Template 收窄为 Topic admin 面（engine 方言可用
// 性判据：kafka 协议族两引擎在册；nats 返回 nil = 受理拒文案面）。
func TopicAdminFaceOf(tpl Template) TopicAdminFace {
	face, ok := tpl.(TopicAdminFace)
	if !ok {
		return nil
	}
	return face
}

// StreamAdminFaceOf 把绑版 Template 收窄为 Stream admin 面（nats 在册；
// kafka 协议族返回 nil）。
func StreamAdminFaceOf(tpl Template) StreamAdminFace {
	face, ok := tpl.(StreamAdminFace)
	if !ok {
		return nil
	}
	return face
}

// —— kafka 实现（kafkaTemplate；镜像自带 /opt/kafka/bin 工具集） ——

// kafkaTopicToolBase 拼 kafka-topics.sh 公共段（凭证经 --command-config 材料
// 文件）。
func (k kafkaTemplate) kafkaTopicToolBase(host, password string) ([]string, map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, nil, err
	}
	return []string{
		"/opt/kafka/bin/kafka-topics.sh",
		"--bootstrap-server", fmt.Sprintf("%s:%d", host, k.Port),
		"--command-config", "/run/secrets/" + kafkaClientPropsFile,
	}, map[string][]byte{kafkaClientPropsFile: KafkaClientPropsFile(password)}, nil
}

// InspectTopic：`kafka-topics.sh --describe --topic NAME`（首行解析）。
func (k kafkaTemplate) InspectTopic(host, password, topic string) (AdminSpec, error) {
	base, secrets, err := k.kafkaTopicToolBase(host, password)
	if err != nil {
		return AdminSpec{}, err
	}
	if err := ValidateTopicName(topic); err != nil {
		return AdminSpec{}, err
	}
	argv := append(append([]string(nil), base...), "--describe", "--topic", topic)
	return AdminSpec{Argv: argv, SecretFiles: secrets}, nil
}

// ParseTopicInspect：退出错 + "does not exist" → 缺席；退出 0 → 首行解析
// （PartitionCount + Configs 段的 retention.ms）。
func (kafkaTemplate) ParseTopicInspect(stdout, stderr string, runErr error) (TopicLive, error) {
	if runErr != nil {
		if strings.Contains(stderr, "does not exist") || strings.Contains(stdout, "does not exist") {
			return TopicLive{Exists: false}, nil
		}
		return TopicLive{}, fmt.Errorf("topic describe failed: %w%s", runErr, adminTail(stderr, stdout))
	}
	return parseKafkaTopicDescribe(stdout)
}

// parseKafkaTopicDescribe 解析 describe 首行：
// `Topic: NAME\tTopicId: …\tPartitionCount: N\tReplicationFactor: R\tConfigs: a=1,retention.ms=MS`。
func parseKafkaTopicDescribe(out string) (TopicLive, error) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "Topic:") {
			continue
		}
		fields := strings.Split(line, "\t")
		live := TopicLive{Exists: true}
		for _, f := range fields {
			key, value, ok := strings.Cut(f, ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			switch strings.TrimSpace(key) {
			case "PartitionCount":
				n, err := strconv.ParseInt(value, 10, 32)
				if err != nil {
					return TopicLive{}, fmt.Errorf("parse PartitionCount %q: %w", value, err)
				}
				live.Partitions = int32(n)
			case "Configs":
				for _, cfg := range strings.Split(value, ",") {
					cfgKey, cfgValue, ok := strings.Cut(cfg, "=")
					if ok && strings.TrimSpace(cfgKey) == "retention.ms" {
						ms, err := strconv.ParseInt(strings.TrimSpace(cfgValue), 10, 64)
						if err != nil {
							return TopicLive{}, fmt.Errorf("parse retention.ms %q: %w", cfgValue, err)
						}
						live.RetentionMS = &ms
					}
				}
			}
		}
		return live, nil
	}
	return TopicLive{}, fmt.Errorf("topic describe output carries no summary line")
}

// CreateTopic：`--create --partitions N --replication-factor 1 [--config retention.ms=MS]`。
func (k kafkaTemplate) CreateTopic(host, password, topic string, partitions int32, retentionMS *int64) (AdminSpec, error) {
	base, secrets, err := k.kafkaTopicToolBase(host, password)
	if err != nil {
		return AdminSpec{}, err
	}
	if err := ValidateTopicName(topic); err != nil {
		return AdminSpec{}, err
	}
	argv := append(append([]string(nil), base...),
		"--create", "--topic", topic,
		"--partitions", strconv.FormatInt(int64(partitions), 10),
		"--replication-factor", "1")
	if retentionMS != nil {
		argv = append(argv, "--config", "retention.ms="+strconv.FormatInt(*retentionMS, 10))
	}
	return AdminSpec{Argv: argv, SecretFiles: secrets}, nil
}

// AlterTopicPartitions：`--alter --partitions <to>`（kafka 旗标是目标值；
// 引擎拒绝减分区——受理半边已拦，此处引擎退出错是纵深）。
func (k kafkaTemplate) AlterTopicPartitions(host, password, topic string, from, to int32) (AdminSpec, error) {
	base, secrets, err := k.kafkaTopicToolBase(host, password)
	if err != nil {
		return AdminSpec{}, err
	}
	if err := ValidateTopicName(topic); err != nil {
		return AdminSpec{}, err
	}
	argv := append(append([]string(nil), base...),
		"--alter", "--topic", topic,
		"--partitions", strconv.FormatInt(int64(to), 10))
	return AdminSpec{Argv: argv, SecretFiles: secrets}, nil
}

// AlterTopicRetention：kafka-configs.sh topic 面 `--add-config retention.ms=MS`。
func (k kafkaTemplate) AlterTopicRetention(host, password, topic string, retentionMS int64) (AdminSpec, error) {
	if err := validatePassword(password); err != nil {
		return AdminSpec{}, err
	}
	if err := ValidateTopicName(topic); err != nil {
		return AdminSpec{}, err
	}
	return AdminSpec{
		Argv: []string{
			"/opt/kafka/bin/kafka-configs.sh",
			"--bootstrap-server", fmt.Sprintf("%s:%d", host, k.Port),
			"--command-config", "/run/secrets/" + kafkaClientPropsFile,
			"--alter", "--entity-type", "topics", "--entity-name", topic,
			"--add-config", "retention.ms=" + strconv.FormatInt(retentionMS, 10),
		},
		SecretFiles: map[string][]byte{kafkaClientPropsFile: KafkaClientPropsFile(password)},
	}, nil
}

// DeleteTopic：`--delete --topic NAME`（kafka 删除是异步标记 + 段清理，
// 数据面即刻停止服务）。
func (k kafkaTemplate) DeleteTopic(host, password, topic string) (AdminSpec, error) {
	base, secrets, err := k.kafkaTopicToolBase(host, password)
	if err != nil {
		return AdminSpec{}, err
	}
	if err := ValidateTopicName(topic); err != nil {
		return AdminSpec{}, err
	}
	argv := append(append([]string(nil), base...), "--delete", "--topic", topic)
	return AdminSpec{Argv: argv, SecretFiles: secrets}, nil
}

// ValidateTopicPartitionsChange：kafka 分区可增不可减（引擎约束受理半边）。
func (kafkaTemplate) ValidateTopicPartitionsChange(from, to int32) error {
	if to < from {
		return fmt.Errorf("pubtemplate: kafka partitions only increase (topic has %d, requested %d); decreasing partitions reorders message keys and is refused by the engine — create a new topic and re-publish to shrink", from, to)
	}
	return nil
}

// —— redpanda 实现（redpandaTemplate；rpk 自带，凭证住 profile 材料文件） ——

// rpTopicBase 拼 rpk 公共段（brokers/凭证住 profile）。
func (redpandaTemplate) rpTopicBase(host, password string) ([]string, map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, nil, err
	}
	return []string{"--config", "/run/secrets/" + redpandaClientFile},
		map[string][]byte{redpandaClientFile: redpandaProfileFile(host, password)}, nil
}

// InspectTopic：`rpk topic describe NAME`（SUMMARY + CONFIGS 表文本解析；
// 缺主题 = 退出 0 且 SUMMARY 空——live 定案）。
func (t redpandaTemplate) InspectTopic(host, password, topic string) (AdminSpec, error) {
	base, secrets, err := t.rpTopicBase(host, password)
	if err != nil {
		return AdminSpec{}, err
	}
	if err := ValidateTopicName(topic); err != nil {
		return AdminSpec{}, err
	}
	argv := append([]string{"rpk", "topic", "describe", topic}, base...)
	return AdminSpec{Argv: argv, SecretFiles: secrets}, nil
}

// ParseTopicInspect：退出 0 + 空 SUMMARY（无 NAME 行）→ 缺席；否则解析
// PARTITIONS 行 + CONFIGS 表的 retention.ms（值面无论 SOURCE——显式与
// 缺省都回读，ensure 段按行期望对照）。
func (redpandaTemplate) ParseTopicInspect(stdout, stderr string, runErr error) (TopicLive, error) {
	if runErr != nil {
		return TopicLive{}, fmt.Errorf("topic describe failed: %w%s", runErr, adminTail(stderr, stdout))
	}
	if !strings.Contains(stdout, "\nNAME") && !strings.HasPrefix(stdout, "NAME") {
		return TopicLive{Exists: false}, nil
	}
	live := TopicLive{Exists: true}
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 2 && fields[0] == "PARTITIONS":
			n, err := strconv.ParseInt(fields[1], 10, 32)
			if err != nil {
				return TopicLive{}, fmt.Errorf("parse PARTITIONS %q: %w", fields[1], err)
			}
			live.Partitions = int32(n)
		case len(fields) >= 2 && fields[0] == "retention.ms":
			ms, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return TopicLive{}, fmt.Errorf("parse retention.ms %q: %w", fields[1], err)
			}
			live.RetentionMS = &ms
		}
	}
	if live.Partitions == 0 {
		return TopicLive{}, fmt.Errorf("topic describe output carries no PARTITIONS row")
	}
	return live, nil
}

// CreateTopic：`rpk topic create NAME --partitions N [-c retention.ms=MS]`。
func (t redpandaTemplate) CreateTopic(host, password, topic string, partitions int32, retentionMS *int64) (AdminSpec, error) {
	base, secrets, err := t.rpTopicBase(host, password)
	if err != nil {
		return AdminSpec{}, err
	}
	if err := ValidateTopicName(topic); err != nil {
		return AdminSpec{}, err
	}
	argv := append([]string{"rpk", "topic", "create", topic,
		"--partitions", strconv.FormatInt(int64(partitions), 10)}, base...)
	if retentionMS != nil {
		argv = append(argv, "-c", "retention.ms="+strconv.FormatInt(*retentionMS, 10))
	}
	return AdminSpec{Argv: argv, SecretFiles: secrets}, nil
}

// AlterTopicPartitions：`rpk topic add-partitions NAME -n <delta>`（live
// 实测定案：-n/--num 是**增量**非目标值——3 分区主题 add -n 5 后 describe
// PARTITIONS 8；from 取实况分区数，delta = to - from）。可增不可减同 kafka。
func (t redpandaTemplate) AlterTopicPartitions(host, password, topic string, from, to int32) (AdminSpec, error) {
	base, secrets, err := t.rpTopicBase(host, password)
	if err != nil {
		return AdminSpec{}, err
	}
	if err := ValidateTopicName(topic); err != nil {
		return AdminSpec{}, err
	}
	if to-from <= 0 {
		return AdminSpec{}, fmt.Errorf("pubtemplate: redpanda add-partitions carries a non-positive delta (%d -> %d)", from, to)
	}
	argv := append([]string{"rpk", "topic", "add-partitions", topic,
		"-n", strconv.FormatInt(int64(to-from), 10)}, base...)
	return AdminSpec{Argv: argv, SecretFiles: secrets}, nil
}

// AlterTopicRetention：`rpk topic alter-config NAME --set retention.ms=MS`。
func (t redpandaTemplate) AlterTopicRetention(host, password, topic string, retentionMS int64) (AdminSpec, error) {
	base, secrets, err := t.rpTopicBase(host, password)
	if err != nil {
		return AdminSpec{}, err
	}
	if err := ValidateTopicName(topic); err != nil {
		return AdminSpec{}, err
	}
	argv := append([]string{"rpk", "topic", "alter-config", topic,
		"--set", "retention.ms=" + strconv.FormatInt(retentionMS, 10)}, base...)
	return AdminSpec{Argv: argv, SecretFiles: secrets}, nil
}

// DeleteTopic：`rpk topic delete NAME`。
func (t redpandaTemplate) DeleteTopic(host, password, topic string) (AdminSpec, error) {
	base, secrets, err := t.rpTopicBase(host, password)
	if err != nil {
		return AdminSpec{}, err
	}
	if err := ValidateTopicName(topic); err != nil {
		return AdminSpec{}, err
	}
	argv := append([]string{"rpk", "topic", "delete", topic}, base...)
	return AdminSpec{Argv: argv, SecretFiles: secrets}, nil
}

// ValidateTopicPartitionsChange：redpanda 分区可增不可减（kafka 协议族
// 同约束——redpanda 镜像同拒减）。
func (redpandaTemplate) ValidateTopicPartitionsChange(from, to int32) error {
	if to < from {
		return fmt.Errorf("pubtemplate: redpanda partitions only increase (topic has %d, requested %d); decreasing partitions reorders message keys and is refused by the engine — create a new topic and re-publish to shrink", from, to)
	}
	return nil
}

// —— nats 实现（natsTemplate；nats-box 工具容器） ——
// InspectStream 复用 StreamDumpFace 既有面（nats.go：`nats stream info
// NAME --json`——admin 面与备份探测面同一命令，零平行渲染）。

// ParseStreamInspect：成功 → JSON 解析；退出错且报文属缺名流模式 → 缺席
// （nats-box 0.15.0 对缺名流报 “could not pick a Stream”/“not found”——
// live 定案两形态都归缺席；其余报文归执行错误）。
func (natsTemplate) ParseStreamInspect(stdout, stderr string, runErr error) (StreamLive, error) {
	if runErr != nil {
		combined := stderr + stdout
		if strings.Contains(combined, "could not pick a Stream") || strings.Contains(strings.ToLower(combined), "not found") {
			return StreamLive{Exists: false}, nil
		}
		return StreamLive{}, fmt.Errorf("stream info failed: %w%s", runErr, adminTail(stderr, stdout))
	}
	var info struct {
		Config struct {
			Subjects  []string `json:"subjects"`
			Retention string   `json:"retention"`
			Storage   string   `json:"storage"`
			MaxBytes  int64    `json:"max_bytes"`
		} `json:"config"`
		State struct {
			Messages int64 `json:"messages"`
			Bytes    int64 `json:"bytes"`
		} `json:"state"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &info); err != nil {
		return StreamLive{}, fmt.Errorf("stream info is not JSON: %w", err)
	}
	if info.Config.Storage == "" {
		return StreamLive{}, fmt.Errorf("stream info carries no config.storage")
	}
	live := StreamLive{
		Exists:    true,
		Subjects:  info.Config.Subjects,
		Retention: info.Config.Retention,
		Storage:   info.Config.Storage,
		Messages:  info.State.Messages,
		Bytes:     info.State.Bytes,
	}
	if info.Config.MaxBytes > 0 {
		live.MaxBytes = info.Config.MaxBytes
		live.MaxBytesSet = true
	}
	return live, nil
}

// CreateStream：`nats stream add NAME --subjects … --retention … --storage …
// --max-bytes N --replicas 1 --defaults`（max-bytes 仅在设置时携带——0 是
// nats 的「未设」哨兵值，显式传 0 会被 CLI 拒）。
func (natsTemplate) CreateStream(host, password, stream string, subjects []string, retention, storage string, maxBytes *int64) (AdminSpec, error) {
	if err := validatePassword(password); err != nil {
		return AdminSpec{}, err
	}
	if err := validateStreamName(stream); err != nil {
		return AdminSpec{}, err
	}
	argv := []string{"nats", "stream", "add", stream,
		"--retention", retention,
		"--storage", storage,
		"--replicas", "1",
		"--defaults",
	}
	if len(subjects) > 0 {
		argv = append(argv, "--subjects", strings.Join(subjects, ","))
	}
	if maxBytes != nil {
		argv = append(argv, "--max-bytes", strconv.FormatInt(*maxBytes, 10))
	}
	return AdminSpec{Argv: argv, Env: natsBoxEnv(host, password)}, nil
}

// UpdateStream：可变面 `--subjects/--max-bytes -f`（retention/storage 创建后
// 不可变——CLI 无旗标；受理位按行值分档拒）。
func (natsTemplate) UpdateStream(host, password, stream string, subjects []string, maxBytes *int64) (AdminSpec, error) {
	if err := validatePassword(password); err != nil {
		return AdminSpec{}, err
	}
	if err := validateStreamName(stream); err != nil {
		return AdminSpec{}, err
	}
	argv := []string{"nats", "stream", "update", stream, "-f"}
	if len(subjects) > 0 {
		argv = append(argv, "--subjects", strings.Join(subjects, ","))
	}
	if maxBytes != nil {
		argv = append(argv, "--max-bytes", strconv.FormatInt(*maxBytes, 10))
	}
	return AdminSpec{Argv: argv, Env: natsBoxEnv(host, password)}, nil
}

// DeleteStream：`nats stream rm NAME -f`（数据删除——wide 档确认后执行）。
func (natsTemplate) DeleteStream(host, password, stream string) (AdminSpec, error) {
	if err := validatePassword(password); err != nil {
		return AdminSpec{}, err
	}
	if err := validateStreamName(stream); err != nil {
		return AdminSpec{}, err
	}
	return AdminSpec{
		Argv: []string{"nats", "stream", "rm", stream, "-f"},
		Env:  natsBoxEnv(host, password),
	}, nil
}

// ValidateTopicName 钳制主题名（kafka 值域：[A-Za-z0-9._-]，非空 ≤249——
// admin argv 的注入面；收敛到无空白/无路径分隔符的保守子集）。
func ValidateTopicName(topic string) error {
	if topic == "" || len(topic) > 249 {
		return fmt.Errorf("pubtemplate: topic name %q is empty or too long", topic)
	}
	for i := 0; i < len(topic); i++ {
		c := topic[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.':
		default:
			return fmt.Errorf("pubtemplate: topic name %q carries characters outside the [A-Za-z0-9._-] set", topic)
		}
	}
	return nil
}

// adminTail 取工具 stderr（缺则 stdout）的末行摘要拼进错误（行上 status_error
// 的取证面——live 实跑教训：只回 "exited with code 1" 丢根因）。
func adminTail(stderr, stdout string) string {
	src := strings.TrimSpace(stderr)
	if src == "" {
		src = strings.TrimSpace(stdout)
	}
	if src == "" {
		return ""
	}
	lines := strings.Split(src, "\n")
	tail := strings.TrimSpace(lines[len(lines)-1])
	if len(lines) > 1 && strings.HasPrefix(tail, "(") {
		// kafka 异常栈尾行形如 "(org.apache.kafka.tools.TopicCommand)"——取上一行实义。
		tail = strings.TrimSpace(lines[len(lines)-2])
	}
	if len(tail) > 240 {
		tail = tail[:240]
	}
	return ": " + tail
}

// TopicDataUnavailableNote 是 kafka 族删除披露的诚实文案锚（CLI/预览共用：
// CLI describe 无分区数据量面——删除披露行不虚标数据量）。
const TopicDataUnavailableNote = "the kafka CLI reports no per-topic byte/message totals; deletion is immediate and the retained segments are unrecoverable"
