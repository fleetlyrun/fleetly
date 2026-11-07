package pubtemplate

// apache/kafka 引擎模板（B6a，ADR-0096 决策 6/7 / design §4.6 kafka 条目 +
// §3.4 旋钮四只）：KRaft combined 单节点（process.roles=broker,controller）
// ——bitnami 排除（Broadcom 变动荡），apache/kafka 官方镜像 4.x 线（3.x
// 不入册，design §2.5）。
//
// 全部 live 定案（2026-11-07 本机 docker 实测 apache/kafka:4.4.0，起服 +
// SASL roundtrip + 热应用 + 轮换 + 重启幂等全绿——实测实录见 kafkadialect.go
// 包注与方法注）：
//   - SASL = SCRAM-SHA-256，引导 = kafka-storage.sh format --add-scram
//     （broker 起前铸用户进 KRaft 元数据；Workload 命令面承载，卷已格式
//     化时 --ignore-formatted 幂等跳过）。
//   - 探针/listener 分面三选一定案 = 内部明文 listener 专供探针
//     （PROBE://127.0.0.1:9094，PLAINTEXT，advertised 127.0.0.1——客户端
//     经 BROKER 面永不学习该 listener）；CONTROLLER listener 兼作探针实测
//     不可行（METADATA 被拒——controller 面只服务 controller RPC）；带凭
//     证探针否（凭证入探针面 = 每探针一材料下发）。探针命令 =
//     /opt/kafka/bin/kafka-broker-api-versions.sh --bootstrap-server
//     localhost:9094（4.4 弃用警告但 4.x 线全线在场；kafka-cluster.sh
//     api-versions 4.3.2 无该子命令不可用；实测 1.2s——探针 Timeout 5s
//     内）。/opt/kafka/bin 不在镜像 PATH，探针用绝对路径。
//   - 传输 PLAINTEXT 内网口径（TLS 内网不现实——zot 明文口径同文化）。
//   - DataTarget = /var/lib/kafka/data（KRaft meta+log 同卷，KAFKA_LOG_DIRS）。
//
// 旋钮四只（design §3.4）：log.retention.ms/message.max.bytes = dynamic
// （kafka-configs.sh 热应用，住 KRaft 元数据重启自愈）；num.partitions =
// static（KAFKA_NUM_PARTITIONS env→server.properties）；heap_mb = static
// env 面（KAFKA_HEAP_OPTS——平台 owned 面暴露的安全子集；heap 与 resources
// 联动立法三分支见 ValidateLinkage/Workload，红队 F-13）。

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// kafkaRepo 是镜像仓库名；kafkaVersions 是在册版本矩阵（digest = 2026-11-07
// Docker Hub index 实测，scripts/digests.sh 同口径对账）。在册政策 = 最新
// 两条 4.x（design §2.5：4.0/4.1 建议值随上游 4.x 线演进为 4.3/4.4——
// Default=全最新）。
const kafkaRepo = "apache/kafka"

var kafkaVersions = []enginetemplate.VersionSpec{
	{
		ID: "4.3", Tag: "4.3.2",
		Digest: "sha256:852a0d49fc4e656b022e7f23785411802c9a088c78a7c5e39f33969de9c227a7",
	},
	{
		ID: "4.4", Tag: "4.4.0",
		Digest:  "sha256:833b867a9585a141da455dc2b57431c65cbccb27f11c91f694444cca9f777e0f",
		Default: true,
	},
}

// kafkaDataTarget 是数据卷容器内挂载目标（KRaft meta+log 同卷）。
const kafkaDataTarget = "/var/lib/kafka/data"

// kafkaClusterID 是平台固定的 KRaft 集群 ID（单节点 combined 自成集群，
// 实例间永不互通——cluster id 只标识元数据日志，固定常量保证渲染幂等；
// 卷异源由 ADR-0097 实例标记门 fail-closed，不依赖 cluster id 隔离）。
const kafkaClusterID = "_x7S_h9HSyetoqJdbEMdSg"

// kafkaMaterialJAAS/kafkaMaterialScram 是 Workload 凭证材料文件名（容器内
// /run/secrets/<名>）：JAAS 给 broker JVM（KAFKA_OPTS 指入）；scram 裸密码
// 给首启 format --add-scram 引导（kafka-storage.sh 无 JAAS 面）。
const (
	kafkaMaterialJAAS  = "database-kafka-jaas"
	kafkaMaterialScram = "database-kafka-scram"
)

// kafkaClientPropsFile 是工具容器 --command-config 材料文件名（dynamic 热
// 应用/轮换/console 工具共用——ApplyDynamicSettings/RotatePassword 的
// SecretFiles 键）。
const kafkaClientPropsFile = "database-kafka-client"

// kafkaKnobs 是 kafka 首批旋钮（design §3.4 四只；键 = 引擎原生词——
// log.retention.ms/message.max.bytes 直用 broker 配置名，num.partitions/
// heap_mb 用平台合成名，Kafka 无段命名空间故不设前缀）。dynamic 两键带
// Default（dynamicResets 的移除复位数据源——KRaft 动态配置不随行删除回退，
// 复位须显式写缺省；缺省值 = 引擎原生缺省 168h/1048588 字节）。
var kafkaKnobs = []enginetemplate.Knob{
	{Key: "log.retention.ms", Kind: enginetemplate.KnobDuration, Class: enginetemplate.KnobDynamic,
		Default: "168h", Category: "persistence"},
	{Key: "message.max.bytes", Kind: enginetemplate.KnobSize, Class: enginetemplate.KnobDynamic,
		Default: "1048588b", Category: "persistence"},
	{Key: "num.partitions", Kind: enginetemplate.KnobInt, Class: enginetemplate.KnobStatic,
		Min: "1", Default: "1", Category: "topics"},
	{Key: "heap_mb", Kind: enginetemplate.KnobSize, Class: enginetemplate.KnobStatic,
		Min: "128mb", Category: "memory"},
}

// kafkaOwnedKeys 是 kafka 的平台 owned 键闭集（design §3.1 owned 清单的
// kafka 映射）：认证（SASL/JAAS/super.users）、监听与通告（listeners/
// advertised/protocol map/inter-broker）、数据目录（log.dirs）、KRaft 集群
// 面（process.roles/node.id/quorum voters/controller listener）、broker 面
// （broker.id/acks/replica 因子类）——旋钮面收窄到 num.partitions，其余
// 全部平台渲染。
var kafkaOwnedKeys = []string{
	// 认证与授权（SASL 凭证/JAAS/超级用户/授权器——平台凭证单源）
	"sasl", "sasl.enabled.mechanisms", "sasl.mechanism.inter.broker.protocol",
	"super.users", "authorizer.class.name", "allow.everyone.if.no.acl.found",
	// 监听与通告（三 listener 分面是平台所有权——探针面/控制面/客户端面）
	"listeners", "advertised.listeners", "listener.security.protocol.map",
	"inter.broker.listener.name", "controller.listener.names",
	"advertised.host.name", "advertised.port", "host.name", "port",
	// KRaft 集群面（单节点 combined 拓扑是平台裁定）
	"process.roles", "node.id", "broker.id", "controller.quorum.voters",
	"controller.quorum.append.linger.ms", "initial.broker.registration.timeout.ms",
	// 数据目录与卷布局（log.dirs = DataTarget 同源）
	"log.dirs", "log.dir", "metadata.log.dir",
	// 复制与存储引擎面（单 broker 事实——副本因子恒 1 是目录标注而非旋钮）
	"default.replication.factor", "min.insync.replicas", "offsets.topic.replication.factor",
	"transaction.state.log.replication.factor", "transaction.state.log.min.isr",
	"share.coordinator.state.topic.replication.factor", "share.coordinator.state.topic.min.isr",
	// 文件包含与身份面
	"include", "meta.properties",
}

// kafkaTemplate 是 kafka 引擎的模板 adapter（嵌 KafkaDialect 方言基座——
// 连接/注入形态复用；B6b redpanda 嵌同基座只覆盖载体面）。
type kafkaTemplate struct {
	KafkaDialect
	version enginetemplate.VersionSpec
}

// Knobs 返回 kafka 旋钮白名单（副本防调用方改注册表）。
func (kafkaTemplate) Knobs() []enginetemplate.Knob {
	out := make([]enginetemplate.Knob, len(kafkaKnobs))
	copy(out, kafkaKnobs)
	return out
}

// OwnedKeys 返回 kafka 平台 owned 键闭集（副本同上）。
func (kafkaTemplate) OwnedKeys() []string {
	out := make([]string, len(kafkaOwnedKeys))
	copy(out, kafkaOwnedKeys)
	return out
}

func (kafkaTemplate) Engine() string { return "kafka" }
func (kafkaTemplate) Versions() []enginetemplate.VersionSpec {
	out := make([]enginetemplate.VersionSpec, len(kafkaVersions))
	copy(out, kafkaVersions)
	return out
}
func (kafkaTemplate) bindVersion(spec enginetemplate.VersionSpec) Template {
	return kafkaTemplate{KafkaDialect: KafkaDialect{Port: 9092}, version: spec}
}
func (t kafkaTemplate) Meta() Info { return Info{Version: t.version.ID, Port: 9092} }
func (t kafkaTemplate) Image() string {
	return pinnedRef(kafkaRepo+":"+t.version.Tag, t.version.Digest)
}
func (t kafkaTemplate) ImageDigest() string { return t.version.Digest }
func (kafkaTemplate) DataTarget() string    { return kafkaDataTarget }
func (kafkaTemplate) DataCapabilities() enginetemplate.DataCapabilities {
	// 引擎级能力位（B6a，design §4.5 L4 家族特化表）：kafka v1 无数据备份
	// ——无官方 dump 工具；backup=false ⇒ migrate=false（诚实推论：亦无
	// dump 基版本迁移——major 升级 = 新空实例或外部工具，目录如实披露）。
	return enginetemplate.DataCapabilities{Backup: false}
}

// ErrNoDataBackupFace 是「本引擎无数据备份面（挂账）」哨兵（design §4.5：
// TriggerBackup 受理位映射 E_INVALID_ARGUMENT；Backup/Restore 契约面对
// kafka 结构性不适用——nats ErrStreamScopedBackup 同族先例）。
var ErrNoDataBackupFace = errors.New("pubtemplate: kafka carries no data backup face (deferred: no official dump tooling — the data plane is rebuilt from stream re-delivery or carried by external tooling)")

// Backup 对 kafka 不适用（v1 挂账——哨兵文案即设计事实；密码仍过渲染闸）。
func (kafkaTemplate) Backup(host, password string) (enginetemplate.BackupSpec, error) {
	_ = host
	if err := validatePassword(password); err != nil {
		return enginetemplate.BackupSpec{}, err
	}
	return enginetemplate.BackupSpec{}, ErrNoDataBackupFace
}

// Restore 对 kafka 不适用（同上——无备份面即无恢复面）。
func (kafkaTemplate) Restore(host, password string) (enginetemplate.RestoreSpec, error) {
	_ = host
	if err := validatePassword(password); err != nil {
		return enginetemplate.RestoreSpec{}, err
	}
	return enginetemplate.RestoreSpec{}, ErrNoDataBackupFace
}

// kafkaBootstrapScript 是 Workload 命令面（镜像 CMD 覆盖——entrypoint
// /__cacert_entrypoint.sh exec "$@" 透传）：①渲染 format 最小属性（九键
// 自 KAFKA_* env——env→properties 正式翻译在 /etc/kafka/docker/run 的
// wrapper setup，format 先行故自带渲染；format 校验链逐键要求在场，live
// 实测：缺 controller.listener.names/listeners/protocol.map/inter-broker/
// advertised 依次被拒）；②kafka-storage.sh format --ignore-formatted
// --add-scram（首启把 SCRAM 用户铸进 KRaft 元数据；已格式化卷幂等跳过
// ——用户持久在元数据，轮换后重启不回滚）；③exec 镜像原生启动链（
// configureDefaults → run：env 翻译 + wrapper setup + broker 启动）。
// bash -c 单串是镜像自带脚本的编排面（argv 数组文化里的声明式例外——
// 链体是平台常量，唯一变量经 env/材料注入，零用户可控拼段）。
var kafkaBootstrapScript = `set -e
. /etc/kafka/docker/bash-config
. /etc/kafka/docker/configureDefaults
printf '%s\n' 'process.roles='"$KAFKA_PROCESS_ROLES" \
  'node.id='"$KAFKA_NODE_ID" \
  'controller.quorum.voters='"$KAFKA_CONTROLLER_QUORUM_VOTERS" \
  'controller.listener.names='"$KAFKA_CONTROLLER_LISTENER_NAMES" \
  'listeners='"$KAFKA_LISTENERS" \
  'advertised.listeners='"$KAFKA_ADVERTISED_LISTENERS" \
  'listener.security.protocol.map='"$KAFKA_LISTENER_SECURITY_PROTOCOL_MAP" \
  'inter.broker.listener.name='"$KAFKA_INTER_BROKER_LISTENER_NAME" \
  'log.dirs='"$KAFKA_LOG_DIRS" > /tmp/fleetly-kafka-format.properties
/opt/kafka/bin/kafka-storage.sh format --ignore-formatted \
  --cluster-id "$CLUSTER_ID" \
  --config /tmp/fleetly-kafka-format.properties \
  --add-scram "` + KafkaSASLMechanism + `=[name=` + KafkaPlatformUser + `,password=$(cat /run/secrets/` + kafkaMaterialScram + `)]"
exec /etc/kafka/docker/run`

// Workload 渲染 kafka 载体面（live 定案最小 env 集 + 命令面 SCRAM 引导）。
// heap 联动三分支（红队 F-13 立法）：heap_mb 设 → KAFKA_HEAP_OPTS 显式
// （受理位 ValidateLinkage 已校验 ≤ memory×0.7）；仅 memory 设 → 渲染期
// 默认 heap=memory×0.5（apache/kafka 缺省 1G 堆在小内存载体 OOMKill 循环
// ——渲染期补正是平台责任）；两者皆无 → 不渲染（镜像缺省 1G）。
// dynamic 键结构性不进本面（喂入律——staticSettings 由调用方预过滤，纵深
// 经 staticKnobSettings 二次收敛）。
func (kafkaTemplate) Workload(host string, memoryMB int64, staticSettings map[string]string) (map[string]string, []string, error) {
	keys, static, err := staticKnobSettings(kafkaKnobs, staticSettings)
	if err != nil {
		return nil, nil, err
	}
	env := map[string]string{
		// KRaft combined 单节点拓扑（live 定案最小集——九键 props 面 + 引导面）
		"KAFKA_NODE_ID":                        "1",
		"KAFKA_PROCESS_ROLES":                  "broker,controller",
		"KAFKA_CONTROLLER_QUORUM_VOTERS":       "1@localhost:9093",
		"KAFKA_CONTROLLER_LISTENER_NAMES":      "CONTROLLER",
		"KAFKA_LISTENERS":                      "BROKER://:9092,CONTROLLER://:9093,PROBE://127.0.0.1:9094",
		"KAFKA_ADVERTISED_LISTENERS":           "BROKER://" + host + ":9092,PROBE://127.0.0.1:9094",
		"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP": "CONTROLLER:PLAINTEXT,BROKER:SASL_PLAINTEXT,PROBE:PLAINTEXT",
		"KAFKA_INTER_BROKER_LISTENER_NAME":     "BROKER",
		// SASL/SCRAM（inter-broker 机制缺省 GSSAPI，起服即拒——live 实测补齐）
		"KAFKA_SASL_ENABLED_MECHANISMS":              "SCRAM-SHA-256",
		"KAFKA_SASL_MECHANISM_INTER_BROKER_PROTOCOL": "SCRAM-SHA-256",
		// JAAS 走文件不走 env（机制名含连字符，env→properties 翻译不可表达）
		"KAFKA_OPTS":     "-Djava.security.auth.login.config=/run/secrets/" + kafkaMaterialJAAS,
		"KAFKA_LOG_DIRS": kafkaDataTarget,
		// KRaft 元数据日志的集群标识（固定常量——幂等渲染，见 kafkaClusterID 注）
		"CLUSTER_ID": kafkaClusterID,
	}
	for _, k := range keys {
		switch k {
		case "num.partitions":
			// static 车道 env 旋钮（§3.2：KAFKA_NUM_PARTITIONS → server.properties
			// 翻译由镜像 wrapper 承载——spec 指纹变即滚动）。
			env["KAFKA_NUM_PARTITIONS"] = static[k]
		case "heap_mb":
			mb, err := knobToMB(static[k])
			if err != nil {
				return nil, nil, err
			}
			env["KAFKA_HEAP_OPTS"] = "-Xms" + mb + "m -Xmx" + mb + "m"
		}
	}
	if _, ok := static["heap_mb"]; !ok && memoryMB > 0 {
		// 仅设 memory：渲染期默认 heap = memory×0.5（F-13 分支二；向下取整，
		// 最低 128m 与旋钮 Min 同源）。
		mb := memoryMB / 2
		if mb < 128 {
			mb = 128
		}
		env["KAFKA_HEAP_OPTS"] = "-Xms" + strconv.FormatInt(mb, 10) + "m -Xmx" + strconv.FormatInt(mb, 10) + "m"
	}
	return env, []string{"/bin/bash", "-c", kafkaBootstrapScript}, nil
}

// knobToMB 把 size 旋钮值折为纯 MB 数字串（heap_mb 渲染核——KAFKA_HEAP_OPTS
// 只收 -Xm<N>m 数字面）。
func knobToMB(v string) (string, error) {
	bytes, err := enginetemplate.KnobSizeBytes(v)
	if err != nil {
		return "", fmt.Errorf("pubtemplate: %w", err)
	}
	return strconv.FormatInt(bytes/1024/1024, 10), nil
}

// Probe 渲染存活探针（live 定案：内部明文 listener 专供探针——9092 SASL
// 面无凭证被拒、9093 controller 面不服务 data-plane API；kafka-broker-api-
// versions 在 4.x 全线在场（4.4 起弃用警告指向 5.x 移除——在册线内可用；
// kafka-cluster.sh api-versions 4.3.2 无子命令不可用）；绝对路径
// （/opt/kafka/bin 不在镜像 PATH）；实测 1.2s（Timeout 5s 内）。
func (kafkaTemplate) Probe() []string {
	return []string{"/opt/kafka/bin/kafka-broker-api-versions.sh", "--bootstrap-server", "localhost:9094"}
}

// Materials 渲染 kafka Workload 凭证材料：JAAS（broker JVM——KafkaServer
// 上下文）+ scram 裸密码（首启 format --add-scram 引导面）。static 旋钮
// 不进材料（num.partitions/heap_mb 走 Workload env 车道——§3.2 static 双
// 面：conf 类住材料、env 类住 env）。密码过渲染安全闸。
func (kafkaTemplate) Materials(password string, staticSettings map[string]string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	if _, _, err := staticKnobSettings(kafkaKnobs, staticSettings); err != nil {
		return nil, err // 纵深：白名单外/坏值不进任何渲染面（env 车道在 Workload 同闸）
	}
	return map[string][]byte{
		kafkaMaterialJAAS:  KafkaJAASFile(password),
		kafkaMaterialScram: []byte(password),
	}, nil
}

// ApplyDynamicSettings 渲染 dynamic 旋钮热应用（live 定案载体：镜像自带
// kafka-configs.sh——AdminClient incrementalAlterConfigs 无 CLI，design
// §3.2 B6 定案即此）：dynamic broker config 住 KRaft 控制器元数据，重启
// 自愈（describe 回读 DYNAMIC_BROKER_CONFIG 同义词）。凭证走 --command-config
// 材料文件（SecretFiles 面）。多旋钮一次渲染 = 一条命令多 k=v（引擎语句
// 原子性承载——部分失败由调用方失败强制 Forget 拉回，ADR-0095 决策 6）。
func (k kafkaTemplate) ApplyDynamicSettings(host, password string, settings map[string]string) (enginetemplate.ApplySpec, error) {
	if err := validatePassword(password); err != nil {
		return enginetemplate.ApplySpec{}, err
	}
	keys, dyn, err := enginetemplate.ValidateDynamicSettings(kafkaKnobs, settings)
	if err != nil {
		return enginetemplate.ApplySpec{}, fmt.Errorf("pubtemplate: %w", err)
	}
	if len(keys) == 0 {
		return enginetemplate.ApplySpec{}, enginetemplate.ErrNoDynamicFace
	}
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		native, err := kafkaNativeValue(k, dyn[k])
		if err != nil {
			return enginetemplate.ApplySpec{}, err
		}
		pairs = append(pairs, k+"="+native)
	}
	return enginetemplate.ApplySpec{
		Argv: []string{
			"/opt/kafka/bin/kafka-configs.sh",
			"--bootstrap-server", fmt.Sprintf("%s:%d", host, k.Port),
			"--command-config", "/run/secrets/" + kafkaClientPropsFile,
			"--alter", "--entity-type", "brokers", "--entity-name", "1",
			"--add-config", strings.Join(pairs, ","),
		},
		SecretFiles: map[string][]byte{kafkaClientPropsFile: KafkaClientPropsFile(password)},
	}, nil
}

// RotatePassword 渲染凭证轮换（live 定案 2026-11-07：数据面改密——以
// current 凭证执行 user alter，新密码立即可用/旧密码即拒；随后 Secret
// 重写由 dataservices 轮换链承载（PG 同序：先数据面后持久层）。SCRAM
// 用户住元数据，轮换不触载体滚动；材料面 JAAS/scram 随收敛环重下发对齐
// （重下发即滚动——载体 spec env/材料指纹变，与「轮换=滚动」的 ADR-0096
// 决策 4 论证一致）。current/next 均过渲染闸。
func (k kafkaTemplate) RotatePassword(host, current, next string) (enginetemplate.RotateSpec, error) {
	if err := validatePassword(current); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	if err := validatePassword(next); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	return enginetemplate.RotateSpec{
		Argv: []string{
			"/opt/kafka/bin/kafka-configs.sh",
			"--bootstrap-server", fmt.Sprintf("%s:%d", host, k.Port),
			"--command-config", "/run/secrets/" + kafkaClientPropsFile,
			"--alter", "--entity-type", "users", "--entity-name", KafkaPlatformUser,
			"--add-config", KafkaSASLMechanism + "=[password=" + next + "]",
		},
		// 工具容器以 current 认证执行（RotateSpec 契约：current 是执行凭证）。
		SecretFiles: map[string][]byte{kafkaClientPropsFile: KafkaClientPropsFile(current)},
	}, nil
}

// ValidateLinkage 校验 settings 与资源面的联动（红队 F-13 三分支的受理
// 半边——heap_mb 与 memory 皆设时 heap ≤ memory×0.7）：LinkageValidator
// 可选面，Create/Update 受理位消费。仅设 heap / 仅设 memory 两分支放行
// （仅 memory 的渲染期补正见 Workload；仅 heap = 用户显式裁定）。
func (kafkaTemplate) ValidateLinkage(memoryMB int64, settings map[string]string) error {
	heap, ok := settings["heap_mb"]
	if !ok || memoryMB <= 0 {
		return nil // 分支二/三：放行（heap 未设或 memory 无限制）
	}
	heapBytes, err := enginetemplate.KnobSizeBytes(heap)
	if err != nil {
		return fmt.Errorf("pubtemplate: %w", err)
	}
	limitBytes := int64(float64(memoryMB) * 0.7 * 1024 * 1024)
	if heapBytes > limitBytes {
		return fmt.Errorf("pubtemplate: heap_mb %s exceeds 70%% of the %d MiB memory limit (max heap_mb %.0fmb — the JVM heap must leave room for metaspace, threads and page cache, or the carrier gets OOM-killed in a loop)",
			heap, memoryMB, float64(memoryMB)*0.7)
	}
	return nil
}
