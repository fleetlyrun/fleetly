package pubsubtemplate

// Kafka 协议方言基座（B6a，ADR-0096 决策 6/7 / design §4.6「方言族机制」）：
// 方言族 = 连接与注入形态的复用单元——kafka 与未来 redpanda（B6b）共享
// bootstrap+SASL 连接材料渲染与 FLEETLY_PUB_*_BOOTSTRAP 注入形态；
// `redpandaTemplate` 嵌本基座、只覆盖镜像/配置渲染/探针/版本线/knob 集。
//
// 基座继承律（design §4.6，红队 A15b）：**基座只含连接/注入形态——Dump/
// Load/Apply/Rotate Spec 必须各自覆盖或显式声明不继承**。当前基座逐面
// 裁决（B6a kafka 的 live 定案形态）：
//   - ApplySpec：不继承载体无关实现——kafka 用镜像自带 `kafka-configs.sh
//     --alter`（dynamic broker config 住 KRaft 控制器元数据，重启自愈）；
//     redpanda 镜像无 kafka-configs.sh，B6b 不可继承（rpk 自有配置面或
//     首批挂账，届时在 redpanda adapter 显式声明）。**B6b 已落**：
//     redpanda adapter 显式覆盖为 `rpk cluster config set` 形态（live
//     定案实录与全部覆盖面清单见 redpanda.go 包注）。
//   - Dump/Load：kafka v1 无数据备份（无官方 dump 工具——design §4.5 L4
//     家族特化表），Backup/Restore 返回 ErrNoDataBackupFace 哨兵；基座
//     不提供实现，B6b redpanda 同挂账（backup=false ⇒ migrate=false）。
//   - Rotate：数据面改密（kafka-configs.sh --entity-type users）+ Secret
//     重写——B6a live 定案（2026-11-07 本机 docker 实测：以 current 凭证
//     执行 user alter，新密码立即可用、旧密码即拒；单节点 inter-broker
//     自连走 broker 自持 JAAS，改密窗内无 broker 侧连接面）；基座提供
//     帮手，redpanda 覆盖为自己的工具面。
//
// SASL 机制 live 定案（design §4.6 评审 F-14，2026-11-07 本机 docker 实测
// apache/kafka:4.4.0）：**SCRAM-SHA-256**（设计倾向证实）。PLAIN 静态 JAAS
// 被否：凭证过网明文（传输 PLAINTEXT 内网口径下 sniffable）。SCRAM 引导
// 路径（chicken-and-egg 唯一解——SASL listener 起服后无凭证连不进铸用户，
// CONTROLLER listener 实测不服务 data-plane API）：broker 起前
// `kafka-storage.sh format --ignore-formatted --add-scram 'SCRAM-SHA-256=[
// name=<user>,password=<pw>]'` 把用户铸进 KRaft 元数据（KRaft 3.5+ 用户
// 住元数据；重启幂等——已格式化卷 --ignore-formatted 跳过、用户持久）。

import (
	"fmt"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

// KafkaPlatformUser 是 kafka 协议族平台合成凭证的用户名（单真源；SCRAM
// 引导、JAAS、client.properties 与 ConnMaterial 共用——NATSPlatformUser
// 同款先例）。
const KafkaPlatformUser = "fleetly"

// KafkaSASLMechanism 是 kafka 协议族的 SASL 机制（live 定案 SCRAM-SHA-256；
// 凭证不过网明文——challenge-response，KRaft 3.5+ 凭证住控制器元数据）。
const KafkaSASLMechanism = "SCRAM-SHA-256"

// —— 连接材料方言（k=v 串；写读同源律，bucketstoretemplate.ParseConnMaterial
// 同款纪律） ——

// 方言串的键名（渲染器与回读器共用的字面量单源；分号分隔、等号赋值）。
// G101 纪律：键名字面量非凭证拼接——password 键的值由调用方注入。
const (
	kafkaConnKeyBootstrap = "bootstrap"
	kafkaConnKeyMechanism = "mechanism"
	kafkaConnKeyUser      = "user"
	kafkaConnKeyPassword  = "password"
)

// renderKafkaConnMaterial 铸 kafka 方言串（bootstrap/mechanism/user/password
// 四键固定序——确定性序列化；kafka 无 URL 方言，bootstrap+SASL 凭证即全
// 部连接知识，design §4.4）。
func renderKafkaConnMaterial(m ConnMaterialSpec) string {
	return kafkaConnKeyBootstrap + "=" + m.Bootstrap + ";" +
		kafkaConnKeyMechanism + "=" + m.Mechanism + ";" +
		kafkaConnKeyUser + "=" + m.User + ";" +
		kafkaConnKeyPassword + "=" + m.Password
}

// parseKafkaConnMaterial 从 k=v 方言串回读四段（键缺席/空值/未知键/重复键
// 都精确失败——写读同源律的回读半边）。
func parseKafkaConnMaterial(material string) (ConnMaterialSpec, error) {
	var out ConnMaterialSpec
	seen := map[string]bool{}
	for _, pair := range strings.Split(material, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return ConnMaterialSpec{}, fmt.Errorf("kafka connection material: segment %q is not key=value", pair)
		}
		if seen[key] {
			return ConnMaterialSpec{}, fmt.Errorf("kafka connection material: key %q appears twice", key)
		}
		seen[key] = true
		switch key {
		case kafkaConnKeyBootstrap:
			out.Bootstrap = value
		case kafkaConnKeyMechanism:
			out.Mechanism = value
		case kafkaConnKeyUser:
			out.User = value
		case kafkaConnKeyPassword:
			out.Password = value
		default:
			return ConnMaterialSpec{}, fmt.Errorf("kafka connection material: unknown key %q", key)
		}
	}
	if out.Bootstrap == "" {
		return ConnMaterialSpec{}, fmt.Errorf("kafka connection material carries no bootstrap")
	}
	if out.Mechanism == "" {
		return ConnMaterialSpec{}, fmt.Errorf("kafka connection material carries no mechanism")
	}
	if out.User == "" {
		return ConnMaterialSpec{}, fmt.Errorf("kafka connection material carries no user")
	}
	if out.Password == "" {
		return ConnMaterialSpec{}, fmt.Errorf("kafka connection material carries no password")
	}
	return out, nil
}

// KafkaDialect 是 kafka 协议族的方言基座（嵌入值——零状态；连接/注入形态
// 的复用单元，B6b redpandaTemplate 嵌入承接）。端口随 adapter 由构造方
// 传入（kafka 9092；redpanda 同 9092 但由 adapter 自持）。
type KafkaDialect struct {
	// Port 是客户端 bootstrap 端口（ConnMaterial 的 host:port 面）。
	Port int32
}

// Dialect 报告方言族词面（目录 §2.3 dialect 轴：kafka+redpanda 同族互示
// 兼容，客户端零改动切换——engine 名 = 实现名，方言 = 连接形态复用名）。
func (KafkaDialect) Dialect() string { return "kafka" }

// ConnMaterial 铸 kafka 方言连接材料（bootstrap=host:port + SASL 凭证）。
func (d KafkaDialect) ConnMaterial(host, password string) ConnMaterialSpec {
	return ConnMaterialSpec{
		Bootstrap: fmt.Sprintf("%s:%d", host, d.Port),
		Mechanism: KafkaSASLMechanism,
		User:      KafkaPlatformUser,
		Password:  password,
	}
}

// —— SASL 材料渲染帮手（broker 与工具容器两半边） ——

// KafkaJAASFile 是 broker 侧 JAAS 材料文件名（容器内 /run/secrets/<名>；
// KAFKA_OPTS=-Djava.security.auth.login.config 指入）。走 KafkaServer 上下文
// 而非 listener.name.*.sasl.jaas.config：机制名 SCRAM-SHA-256 含连字符，
// docker env→properties 翻译不可表达该键——JAAS 文件是唯一无连字符键面。
func KafkaJAASFile(password string) []byte {
	// 服务半边校验 SCRAM 凭证（住元数据）；client 半边（username/password）
	// 供 broker 作为 SASL client 的自连（inter-broker listener 在 SASL 面）。
	// 密码过渲染安全闸 [0-9A-Za-z]（dataservicetemplate.ValidatePassword——JAAS
	// 双引号串的注入防线，纵深）。
	return []byte("KafkaServer {\n" +
		"    org.apache.kafka.common.security.scram.ScramLoginModule required\n" +
		"    username=\"" + KafkaPlatformUser + "\"\n" +
		"    password=\"" + password + "\";\n" +
		"};\n")
}

// KafkaClientPropsFile 渲染工具容器（kafka-configs.sh/console 工具/
// ApplyDynamicSettings/RotatePassword）的 --command-config 客户端属性文件：
// SASL_PLAINTEXT + SCRAM-SHA-256 + JAAS 行（客户端 sasl.jaas.config 是
// properties 值——普通键，无连字符键面问题）。
func KafkaClientPropsFile(password string) []byte {
	return []byte("security.protocol=SASL_PLAINTEXT\n" +
		"sasl.mechanism=" + KafkaSASLMechanism + "\n" +
		"sasl.jaas.config=org.apache.kafka.common.security.scram.ScramLoginModule required username=\"" +
		KafkaPlatformUser + "\" password=\"" + password + "\";\n")
}

// KafkaScramBootstrapArg 渲染 `kafka-storage.sh format --add-scram` 的
// 引导参数（SCRAM-SHA-256=[name=<user>,password=<pw>]——首启把平台用户铸
// 进 KRaft 元数据；密码过渲染安全闸）。
func KafkaScramBootstrapArg(password string) string {
	return KafkaSASLMechanism + "=[name=" + KafkaPlatformUser + ",password=" + password + "]"
}

// KafkaDurationToMs 把受理口径 duration 值（<num><ms|s|m|h|d>）折为 kafka
// 原生毫秒整数（log.retention.ms 的渲染核；ValidateKnobValue 已保证形态，
// 此处拆单位——size→bytes 同族帮手）。
func KafkaDurationToMs(v string) (int64, error) {
	var num, unit string
	switch {
	case strings.HasSuffix(v, "ms"):
		num, unit = v[:len(v)-2], "ms"
	case strings.HasSuffix(v, "s"), strings.HasSuffix(v, "m"),
		strings.HasSuffix(v, "h"), strings.HasSuffix(v, "d"):
		num, unit = v[:len(v)-1], v[len(v)-1:]
	default:
		return 0, fmt.Errorf("pubsubtemplate: %q is not a duration", v)
	}
	var n int64
	for i := 0; i < len(num); i++ {
		c := num[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("pubsubtemplate: %q is not a duration", v)
		}
		n = n*10 + int64(c-'0')
	}
	switch unit {
	case "ms":
		return n, nil
	case "s":
		return n * 1000, nil
	case "m":
		return n * 1000 * 60, nil
	case "h":
		return n * 1000 * 60 * 60, nil
	default: // "d"
		return n * 1000 * 60 * 60 * 24, nil
	}
}

// kafkaNativeValue 把受理口径旋钮值折为 kafka 原生字面（dynamic 应用面）：
// duration → 毫秒整数（log.retention.ms）；size → 字节整数
// （message.max.bytes——Kafka wants bytes，mysql SET PERSIST 同款折算）。
// 键不在折算表 = 白名单外 dynamic 键（ValidateDynamicSettings 已拦，防御
// 态诚实拒）。
func kafkaNativeValue(key, value string) (string, error) {
	switch key {
	case "log.retention.ms":
		ms, err := KafkaDurationToMs(value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d", ms), nil
	case "message.max.bytes":
		bytes, err := dataservicetemplate.KnobSizeBytes(value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d", bytes), nil
	default:
		return "", fmt.Errorf("pubsubtemplate: knob %q carries no kafka dynamic-config rendering", key)
	}
}
