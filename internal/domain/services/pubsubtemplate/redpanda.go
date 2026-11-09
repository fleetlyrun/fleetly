package pubsubtemplate

// redpanda 引擎模板（B6b，ADR-0096 决策 7 / design §4.6 redpanda 条目）：
// Kafka 协议族第三方实现——**嵌 KafkaDialect 方言基座**（连接/注入形态复用：
// bootstrap+SASL 材料渲染与 FLEETLY_PUB_*_BOOTSTRAP 注入面同形，目录
// dialect=kafka 同族互示、客户端零改动切换），载体面全自持（镜像/配置渲染/
// 探针/版本线/knob 集）。native 单二进制无 JVM 堆（内存由 rpk 从 cgroup
// 上限自留——resources.memory_mb 天然映射，无 heap 联动面）。
//
// 全部 live 定案（2026-11-07 本机 docker 实测 redpandadata/redpanda:v26.2.6，
// 起服 + SASL roundtrip + 热应用 + 轮换 + 重启幂等 + 优雅关停全绿）：
//   - 配置形态（红队 A4 纠偏落实）：**非 dev-container**——平台 Materials 完整
//     渲染 redpanda.yaml（auto_create_topics_enabled: false 收编 dev 默认；
//     data_directory/rpc/kafka/admin listener/SASL 面；developer_mode: false
//     生产形态实测在容器内起服即绿）。redpanda 无 KAFKA_* 式 env 翻译面，
//     配置文件单源渲染（kafka「九属性单源」的同族精神，载体是 yaml 非 env）。
//     advertised_kafka_api 依赖实例 DNS 名（Materials 签名无 host）→ 经
//     Workload env FLEETLY_ADVERTISE_KAFKA 注入、rpk --advertise-kafka-addr
//     旗标承载（kafka 的 KAFKA_ADVERTISED_LISTENERS env 车道同族形态；live
//     实测：旗标覆盖触发 rpk 向配置同目录写临时文件——ro 材料挂载面必须先
//     cp 到 /tmp 再起服）。
//   - 探针三选一定案 = **admin API 9644 + curl**：镜像自带 /usr/bin/curl 与
//     /usr/bin/rpk（PATH 内）；设计稿假设的 /v1/cluster/health 在 v26 实测
//     404——就绪面 = `/v1/status/ready`（返回 {"status":"ready"}，实测 48ms；
//     该端点与 /v1/cluster/health_overview 同为鉴权豁免面）。探针经 bash -c
//     复合 `test -f 引导完成门 && curl 就绪`：SASL 用户铸造窗内（首启 admin
//     面开放到 user create + 硬化落地为止）探针不绿——instance 不报 running、
//     注入材料不早于铸用户面下发（kafka 带凭证探针否决同律：凭证不入探针面）。
//   - SASL live 定案：`enable_sasl: true` + `superusers: [fleetly]` + SCRAM-
//     SHA-256（rpk 缺省机制，与 KafkaSASLMechanism 基座常量同值）；用户铸造
//     = 起服后 admin API（`rpk acl user create`——admin 面与 kafka 面分立，
//     SASL 起服后 kafka 面无凭证连不进的鸡生蛋在 admin 面有解）。**首启 admin
//     面开放（admin_api_require_auth 渲染缺席）**：live 实测 require_auth 入
//     yaml + 空卷 = 死局（首用户铸造被 401 拒——无 bootstrap 豁免）；铸用户后
//     引导链以 cluster config 翻转 `admin_api_require_auth true`（动态属性住
//     控制器日志随卷持久，重启自持；幂等 `|| true`）。此后 admin 面全程要求
//     superuser 凭证——项目网上的无凭证 admin（铸用户/改配置）暴露窗只在首启
//     铸用户前（探针门保证 instance 不早于此报 running）。
//   - 引导链 = bash -c 监督面：rpk 后台起服 → 有界就绪等待（120×0.5s=60s 超
//     时基线，与探针 StartPeriod 同拍）→ 幂等铸用户（create 失败〔含 require_
//     auth 后的 401〕即落 update 以当前材料密码重铸——重启/轮换后随卷收敛）
//     → 翻转 admin 硬化 → 触引导完成门 → wait。SIGTERM 陷阱转发 rpk；rpk
//     收 TERM 即退（143）不等其 redpanda 子进程优雅收尾——wait 后轮询 pidof
//     redpanda 收养孤儿（PID 1）收尾再退（live 实测：无此环 docker stop 即
//     teardown SIGKILL 截断 fsync，有此环 12 条 Shutdown complete + exit 0）。
//   - dynamic 面 live 定案（方言基座继承律的 Apply 覆盖半边）：redpanda 有
//     **cluster config 动态面**（`rpk cluster config set/get`——属性住控制器
//     日志、重启自愈、snake_case 键名）→ ApplySpec = rpk 形态，**显式不继承**
//     kafka 基座（镜像无 kafka-configs.sh——继承律）。live 核值：log_retention_
//     ms/log_segment_size 可 set/get；**message_max_bytes 非 cluster 属性**
//     （redpanda 消息上限是 topic 级——kafka 族 broker 级旋钮在此诚实缺席）；
//     `config import` 是全态语义（文件缺席键即复位缺省——不可作增量载体），
//     多旋钮 = bash -c 链逐键 `set`（逐键幂等，失败 Forget 重放收敛）。
//   - 轮换 live 定案（继承律的 Rotate 覆盖半边）：`rpk acl user update <user>
//     --new-password <next> --mechanism SCRAM-SHA-256`（admin 面）——数据面
//     改密，实测新密码立即可用/旧密码即拒（kafka SCRAM user alter 同语义）；
//     执行凭证（current）住 rpk profile 材料文件（--config 面，kafka 的
//     client.properties 同族车道；rpk -X user/pass 走 argv 被否）。后续 Secret
//     重写 + 载体材料重下发由 dataservices 轮换链承载（PG 同序）。
//   - Dump/Load：无数据备份面挂账（同 kafka——无官方 dump 工具），Backup/
//     Restore 返回 redpanda 专属哨兵；DataCapabilities{Backup:false} ⇒
//     migrate=false 诚实推论。
//
// 版本矩阵（live 核实 2026-11-07 Docker Hub）：v26 双 minor 线并行维护
// （26.1 补丁线 26.1.20 发布 2026-11-04、26.2 线 26.2.6 发布 2026-10-23）
// ——在册两条，Default=高 minor 线 26.2。BSL 1.1 许可披露见 catalog.go
// （license_note 露出，代码面不设限——托管合规裁定归运营方）。

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

// redpandaRepo 是镜像仓库名；redpandaVersions 是在册版本矩阵（digest =
// 2026-11-07 Docker Hub index 实测，scripts/digests.sh 同口径对账）。26.1/
// 26.2 双活取两条（Default = 26.2——高 minor 线）。
const redpandaRepo = "redpandadata/redpanda"

var redpandaVersions = []dataservicetemplate.VersionSpec{
	{
		ID: "26.1", Tag: "v26.1.20",
		Digest: "sha256:8c94f3135dcb94736744aea8fae1ca46c46c7d26ad219cb2e2c10defbe884872",
	},
	{
		ID: "26.2", Tag: "v26.2.6",
		Digest:  "sha256:8b1076e0d61bfceb66594dc01536427ce0b529f8ead35fdab9763ab6085891ce",
		Default: true,
	},
}

// redpandaDataTarget 是数据卷容器内挂载目标（控制器日志+分片同卷）。
const redpandaDataTarget = "/var/lib/redpanda/data"

// 材料文件名（容器内 /run/secrets/<名>）：config 是平台完整渲染的
// redpanda.yaml（非 dev 模式单源）；scram 是裸密码（起服后 admin 铸用户面，
// kafka 的 database-kafka-scram 同族）；client 是 rpk profile（工具容器
// apply/rotate 的 --config 凭证车道，kafka 的 client.properties 同族）。
const (
	redpandaConfigFile = "database-redpanda-config"
	redpandaScramFile  = "database-redpanda-scram"
	redpandaClientFile = "database-redpanda-client"
)

// redpandaKnobs 是 redpanda 首批旋钮（最小集，live 定案）：cluster config
// 动态面两键（snake_case 引擎原生词——log_retention_ms/log_segment_size，
// 不带点形与 kafka 族点形键刻意分立——两实现旋钮集自持，目录如实各表）。
// 全 dynamic：v1 无 static 旋钮（yaml 面平台 owned，资源面走 resources 轴
// ——rpk 从 cgroup 自留，无 heap 联动）。dynamic 键带 Default（移除复位的
// 数据源；缺省值 = 引擎原生缺省 7d/128mb，live 对账 cluster config list）。
// message.max.bytes 族缺席：redpanda 消息上限是 topic 级属性非 cluster 面
// （live 实测 set 被拒 unknown property——诚实不入册）。
var redpandaKnobs = []dataservicetemplate.Knob{
	{Key: "log_retention_ms", Kind: dataservicetemplate.KnobDuration, Class: dataservicetemplate.KnobDynamic,
		Default: "168h", Category: "persistence"},
	{Key: "log_segment_size", Kind: dataservicetemplate.KnobSize, Class: dataservicetemplate.KnobDynamic,
		Default: "128mb", Category: "persistence"},
}

// redpandaOwnedKeys 是 redpanda 的平台 owned 键闭集（Materials 渲染的
// redpanda.yaml 结构性键 + 引导链裁定面）：数据目录与单节点拓扑、监听与
// 通告分面（kafka_api/admin=平台所有权）、生产形态与 auto_create 收编、
// SASL/超级用户/admin 硬化（凭证单源）。旋钮面（cluster config 动态键）
// 与本闭集不相交。
var redpandaOwnedKeys = []string{
	// 数据目录与集群拓扑（单节点自持是平台裁定）
	"data_directory", "seed_servers", "node_id",
	// 监听与通告（listener 分面 = 平台所有权：客户端面 9092/admin 面 9644）
	"rpc_server", "advertised_rpc_api", "kafka_api", "advertised_kafka_api",
	"admin", "admin_api",
	// 形态与安全基线（生产形态/auto_create 收编/SASL/硬化 = 渲染面）
	"developer_mode", "auto_create_topics_enabled", "enable_sasl",
	"superusers", "admin_api_require_auth",
}

// redpandaTemplate 是 redpanda 引擎的模板 adapter（嵌 KafkaDialect 方言基座
// ——连接/注入形态复用；载体面全自持。继承律逐面裁决：ConnMaterial/Dialect
// 继承基座；Dump/Load/Apply/Rotate 全部覆盖——见各方法注）。
type redpandaTemplate struct {
	KafkaDialect
	dataservicetemplate.ToolUserDefault
	version dataservicetemplate.VersionSpec
}

// ToolUser 声明工具面以 root 运行（B8-fix 主案，kafkaTemplate 同判据）：
// redpandadata/redpanda 镜像 User=redpanda(uid 101)——非 root 用户写
// root 属主新卷根/读 0600 材料（rpk profile --config）均被拒。rpk admin
// 面全部经平台渲染 argv+材料承载，root 运行安全；载体仍按镜像用户跑。
func (redpandaTemplate) ToolUser() string { return "root" }

// Knobs 返回 redpanda 旋钮白名单（副本防调用方改注册表）。
func (redpandaTemplate) Knobs() []dataservicetemplate.Knob {
	out := make([]dataservicetemplate.Knob, len(redpandaKnobs))
	copy(out, redpandaKnobs)
	return out
}

// OwnedKeys 返回 redpanda 平台 owned 键闭集（副本同上）。
func (redpandaTemplate) OwnedKeys() []string {
	out := make([]string, len(redpandaOwnedKeys))
	copy(out, redpandaOwnedKeys)
	return out
}

func (redpandaTemplate) Engine() string { return "redpanda" }
func (redpandaTemplate) Versions() []dataservicetemplate.VersionSpec {
	out := make([]dataservicetemplate.VersionSpec, len(redpandaVersions))
	copy(out, redpandaVersions)
	return out
}
func (redpandaTemplate) bindVersion(spec dataservicetemplate.VersionSpec) Template {
	return redpandaTemplate{KafkaDialect: KafkaDialect{Port: 9092}, version: spec}
}
func (t redpandaTemplate) Meta() Info { return Info{Version: t.version.ID, Port: 9092} }
func (t redpandaTemplate) Image() string {
	return pinnedRef(redpandaRepo+":"+t.version.Tag, t.version.Digest)
}
func (t redpandaTemplate) ImageDigest() string { return t.version.Digest }
func (redpandaTemplate) DataTarget() string    { return redpandaDataTarget }

// DataCapabilities：redpanda v1 无数据备份面（挂账——无官方 dump 工具，
// kafka 同族同裁；backup=false ⇒ migrate=false 诚实推论）。
func (redpandaTemplate) DataCapabilities() dataservicetemplate.DataCapabilities {
	return dataservicetemplate.DataCapabilities{Backup: false}
}

// ErrRedpandaNoDataBackupFace 是 redpanda 的「无数据备份面（挂账）」哨兵
// （kafka 的 ErrNoDataBackupFace 同族；消费面按引擎级能力位门控先拒——
// 受理位文案「no data backup face」锚与 e2e/apitest 共用）。
var ErrRedpandaNoDataBackupFace = fmt.Errorf("pubsubtemplate: redpanda carries no data backup face (deferred: no official dump tooling — the data plane is rebuilt from stream re-delivery or carried by external tooling)")

// Backup 对 redpanda 不适用（v1 挂账；密码仍过渲染闸）。
func (redpandaTemplate) Backup(host, password string) (dataservicetemplate.BackupSpec, error) {
	_ = host
	if err := validatePassword(password); err != nil {
		return dataservicetemplate.BackupSpec{}, err
	}
	return dataservicetemplate.BackupSpec{}, ErrRedpandaNoDataBackupFace
}

// Restore 对 redpanda 不适用（同上——无备份面即无恢复面）。
func (redpandaTemplate) Restore(host, password string) (dataservicetemplate.RestoreSpec, error) {
	_ = host
	if err := validatePassword(password); err != nil {
		return dataservicetemplate.RestoreSpec{}, err
	}
	return dataservicetemplate.RestoreSpec{}, ErrRedpandaNoDataBackupFace
}

// RedpandaConfigFile 渲染平台完整配置 redpanda.yaml（非 dev 模式单源——
// design §4.6 红队 A4 纠偏：dev-container 是官方明示非生产预设，生产形态
// 由平台逐键渲染）。键序固定（确定性序列化——golden 钉板）；advertised_
// kafka_api 不在此面（host 依赖经 Workload env → rpk 旗标，见 Workload 注）。
// memoryMB>0 时 rpk 节追加显式 memory/smp（B8-fix live 实录：rpk 的 cgroup
// 自动探测在受限容器里把 seastar 预算算成 ~58MiB/shard 起服即 OOM 崩环
// ——`docker run -m 2g` 直测可复现；显式 `memory: 1G`+`smp: 1` 同环境起服
// 绿）。memory = limit×0.5（kafka heap F-13 分支二同判据：native 进程留半
// 给页缓存/内核），下限 256M（再小的 seastar 预算不可起服）；smp=1 是
// 单节点小实例的诚实钉板（按核分片会把小预算再切薄）。memoryMB=0（无限
// 制）不追加——B6b 无限制 live 实测自动探测可用，行为不变。
func RedpandaConfigFile(memoryMB int64) []byte {
	rpk := "rpk:\n  overprovisioned: true\n"
	if memoryMB > 0 {
		half := memoryMB / 2
		if half < 256 {
			half = 256
		}
		rpk += "  memory: " + rpMemoryString(half) + "\n  smp: 1\n"
	}
	return []byte(`redpanda:
  data_directory: "` + redpandaDataTarget + `"
  seed_servers: []
  rpc_server:
    address: "0.0.0.0"
    port: 33145
  advertised_rpc_api:
    address: "127.0.0.1"
    port: 33145
  kafka_api:
  - address: "0.0.0.0"
    port: 9092
  admin:
    address: "0.0.0.0"
    port: 9644
  developer_mode: false
  auto_create_topics_enabled: false
  enable_sasl: true
  superusers:
  - "` + KafkaPlatformUser + `"
` + rpk)
}

// rpMemoryString 把 MiB 折为 rpk memory 词法（整 GiB → G、否则 M——
// rpk 的 k/m/g/t 后缀族；2048MiB→1G、768MiB→768M）。
func rpMemoryString(mib int64) string {
	if mib%1024 == 0 {
		return strconv.FormatInt(mib/1024, 10) + "G"
	}
	return strconv.FormatInt(mib, 10) + "M"
}

// redpandaBootstrapScript 是 Workload 命令面（bash -c 监督链——镜像
// entrypoint 会吞并 CMD 前缀 rpk，平台 Workload.Command 是 entrypoint 覆盖
// 语义〔swarm ContainerSpec.Command/k8s command 皆 entrypoint 面〕故 bash
// 直达；live 定案实录见包注）。链体是平台常量，唯一变量经 env/材料注入
// （kafkaBootstrapScript 同律：零用户可控拼段）。
const redpandaBootstrapScript = `set -e
PW="$(cat /run/secrets/` + redpandaScramFile + `)"
rm -f /tmp/fleetly-boot-done
cp /run/secrets/` + redpandaConfigFile + ` /tmp/fleetly-redpanda.yaml
rpk redpanda start --config /tmp/fleetly-redpanda.yaml --check=false --advertise-kafka-addr "$FLEETLY_ADVERTISE_KAFKA" &
RPID=$!
trap 'kill -TERM $RPID 2>/dev/null' TERM INT
i=0
until curl -sf -o /dev/null http://127.0.0.1:9644/v1/status/ready; do
  kill -0 $RPID 2>/dev/null || exit 1
  i=$((i+1)); [ "$i" -ge 120 ] && exit 1
  sleep 0.5
done
if ! rpk acl user create ` + KafkaPlatformUser + ` --password "$PW" --mechanism scram-sha-256 2>/dev/null; then
  RPK_USER=` + KafkaPlatformUser + ` RPK_PASS="$PW" rpk acl user update ` + KafkaPlatformUser + ` --new-password "$PW" --mechanism SCRAM-SHA-256
fi
RPK_USER=` + KafkaPlatformUser + ` RPK_PASS="$PW" rpk cluster config set admin_api_require_auth true >/dev/null 2>&1 || true
touch /tmp/fleetly-boot-done
wait $RPID || true
while pidof redpanda >/dev/null 2>&1; do sleep 0.5; done`

// Workload 渲染 redpanda 载体面：env 唯一键 = FLEETLY_ADVERTISE_KAFKA
// （实例网内名:9092——rpk --advertise-kafka-addr 消费；redpanda 无 KAFKA_*
// 式 env 翻译面，配置主体住材料 yaml）。memoryMB 忽略：native 无 JVM 堆，
// rpk 从 cgroup 内存上限自留（resources 轴天然映射——无 heap 联动分支、
// 不实现 LinkageValidator）。v1 无 static 旋钮（staticKnobSettings 纵深
// 闸照走——白名单外/坏值不进任何渲染面）。
func (redpandaTemplate) Workload(host string, memoryMB int64, staticSettings map[string]string) (map[string]string, []string, error) {
	if _, _, err := staticKnobSettings(redpandaKnobs, staticSettings); err != nil {
		return nil, nil, err
	}
	env := map[string]string{
		"FLEETLY_ADVERTISE_KAFKA": fmt.Sprintf("%s:%d", host, 9092),
	}
	return env, []string{"/bin/bash", "-c", redpandaBootstrapScript}, nil
}

// Probe 渲染存活探针（live 定案：admin API 9644 /v1/status/ready + curl
// ——鉴权豁免端点，admin 硬化后仍可达；复合引导完成门 /tmp/fleetly-boot-done
// ——铸用户+硬化落地前探针不绿，首启 admin 开放窗不随 running 暴露）。
func (redpandaTemplate) Probe() []string {
	return []string{"/bin/bash", "-c",
		"test -f /tmp/fleetly-boot-done && curl -sf -o /dev/null http://127.0.0.1:9644/v1/status/ready"}
}

// Materials 渲染 redpanda Workload 材料：完整 redpanda.yaml（平台单源；
// memoryMB>0 时 rpk.memory/smp 显式化——自动探测在受限容器崩环，见
// RedpandaConfigFile 注）+ scram 裸密码（起服后 admin 铸用户）。v1 无
// static 旋钮（纵深闸照走——空设置产物恒定）。密码过渲染安全闸。
func (redpandaTemplate) Materials(password string, memoryMB int64, staticSettings map[string]string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	if _, _, err := staticKnobSettings(redpandaKnobs, staticSettings); err != nil {
		return nil, err
	}
	return map[string][]byte{
		redpandaConfigFile: RedpandaConfigFile(memoryMB),
		redpandaScramFile:  []byte(password),
	}, nil
}

// redpandaProfileFile 渲染工具容器的 rpk profile（--config 凭证车道：
// kafka_api SCRAM 面 + admin_api 地址；kafka 的 KafkaClientPropsFile 同族
// ——argv 不载凭证）。profile 键名 live 定案（version 7 形态实测认证可达
// kafka 面 9092 与 admin 面 9644）。
func redpandaProfileFile(host, password string) []byte {
	return []byte(`version: 7
current_profile: ` + KafkaPlatformUser + `
profiles:
  - name: ` + KafkaPlatformUser + `
    kafka_api:
      brokers:
        - "` + host + `:9092"
      sasl:
        user: "` + KafkaPlatformUser + `"
        password: "` + password + `"
    admin_api:
      addresses:
        - "` + host + `:9644"
`)
}

// ApplyDynamicSettings 渲染 dynamic 旋钮热应用（继承律 Apply 半边的**显式
// 覆盖**：rpk cluster config set——redpanda 自有动态面，不继承 kafka 基座
// 的 kafka-configs.sh，镜像亦无该工具）。值折算：duration→ms 整数
// （KafkaDurationToMs 基座帮手）/ size→bytes 整数（KnobSizeBytes）；键名
// 直用 snake_case 原生词。多旋钮 = bash -c 链逐键 set（rpk set 单键单命令；
// config import 是全态复位语义不可用——逐键幂等，部分失败由调用方 Forget
// 重放收敛）。凭证住 profile 材料文件。
func (redpandaTemplate) ApplyDynamicSettings(host, password string, settings map[string]string) (dataservicetemplate.ApplySpec, error) {
	if err := validatePassword(password); err != nil {
		return dataservicetemplate.ApplySpec{}, err
	}
	keys, dyn, err := dataservicetemplate.ValidateDynamicSettings(redpandaKnobs, settings)
	if err != nil {
		return dataservicetemplate.ApplySpec{}, fmt.Errorf("pubsubtemplate: %w", err)
	}
	if len(keys) == 0 {
		return dataservicetemplate.ApplySpec{}, dataservicetemplate.ErrNoDynamicFace
	}
	cmds := make([]string, 0, len(keys))
	for _, k := range keys {
		native, err := redpandaNativeValue(k, dyn[k])
		if err != nil {
			return dataservicetemplate.ApplySpec{}, err
		}
		cmds = append(cmds, fmt.Sprintf("rpk cluster config set %s %s --config /run/secrets/%s", k, native, redpandaClientFile))
	}
	return dataservicetemplate.ApplySpec{
		Argv:        []string{"/bin/bash", "-c", strings.Join(cmds, " && ")},
		SecretFiles: map[string][]byte{redpandaClientFile: redpandaProfileFile(host, password)},
	}, nil
}

// redpandaNativeValue 把受理口径旋钮值折为 redpanda 原生字面（cluster
// config 整数面：duration→ms / size→bytes——kafkaNativeValue 同族折算，
// 键名为 snake_case）。键不在折算表 = 白名单外 dynamic 键（防御态诚实拒）。
func redpandaNativeValue(key, value string) (string, error) {
	switch key {
	case "log_retention_ms":
		ms, err := KafkaDurationToMs(value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d", ms), nil
	case "log_segment_size":
		bytes, err := dataservicetemplate.KnobSizeBytes(value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d", bytes), nil
	default:
		return "", fmt.Errorf("pubsubtemplate: knob %q carries no redpanda cluster-config rendering", key)
	}
}

// RotatePassword 渲染凭证轮换（继承律 Rotate 半边的覆盖：`rpk acl user
// update <user> --new-password <next> --mechanism SCRAM-SHA-256`——admin 面
// 数据面改密，live 定案新密码立即可用/旧密码即拒；kafka SCRAM user alter
// 同语义、PG 同序——先数据面后 Secret 重写）。执行凭证（current）住 profile
// 材料文件（argv 只载 next——kafka 轮换面同形）。current/next 均过渲染闸。
func (redpandaTemplate) RotatePassword(host, current, next string) (dataservicetemplate.RotateSpec, error) {
	if err := validatePassword(current); err != nil {
		return dataservicetemplate.RotateSpec{}, err
	}
	if err := validatePassword(next); err != nil {
		return dataservicetemplate.RotateSpec{}, err
	}
	return dataservicetemplate.RotateSpec{
		Argv: []string{
			"rpk", "acl", "user", "update", KafkaPlatformUser,
			"--new-password", next,
			"--mechanism", KafkaSASLMechanism,
			"--config", "/run/secrets/" + redpandaClientFile,
		},
		// 工具容器以 current 认证执行（RotateSpec 契约：current 是执行凭证）。
		SecretFiles: map[string][]byte{redpandaClientFile: redpandaProfileFile(host, current)},
	}, nil
}
