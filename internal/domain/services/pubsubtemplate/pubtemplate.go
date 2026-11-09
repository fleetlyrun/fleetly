// Package pubsubtemplate 是 PubSub 类型模板注册表（B4b，ADR-0096 决策 1/3/7；
// design §4.1 L2 类型/引擎声明层）：engine 名 → 版本矩阵（Versions）+ 端口 +
// 数据卷目标 + 凭证材料渲染方式。形状与 databasetemplate 同构（dataservicetemplate
// 共享内核的类型消费面）；**kafka 方言基座 B6a 已立**（KafkaDialect——
// 连接/注入形态的复用单元，B6b redpanda 嵌入承接，继承律见 kafkadialect.go）。
//
// 凭证/连接方言（ADR-0096 决策 3/4）：连接材料渲染取代 URL 假设——nats
// 出 ConnMaterial = `nats://<user>:<pw>@<host>:4222`（URL 形）；kafka 出
// bootstrap/mechanism/user/password k=v 串。Secret 值 = 类型方言串
// （RenderConnMaterial），渲染器/回读器同源（ParseConnMaterial 回读，
// 写读同源律）。凭证名 `pubsub:<name>`（与 database:<name> 同先例）。
//
// 本包 stdlib-only 近叶子（除 dataservicetemplate 内核外无 internal 依赖——
// import 守卫同 databasetemplate 口径）；镜像 digest 钉定门禁
// （TestImageDigestsPinned/TestTemplateFaces 形）见 pubsubtemplate_test.go。
package pubsubtemplate

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

// Info 是 API 回显面的模板信息（version/port；与 databasetemplate.Info 同形）。
type Info struct {
	Version string
	Port    int32
}

// ConnMaterialSpec 是 pubsub 类型连接材料的结构面（design §4.4「连接材料
// 渲染取代 URL 假设」——B6a kafka 接入后双方言并存）：nats 方言只填 URL
// （URL 形）；kafka 方言填 Bootstrap/Mechanism/User/Password（无 URL 方言
// ——bootstrap+SASL 凭证即全部连接知识）。Secret 值 = 方言串
// （RenderConnMaterial）；注入面消费结构字段（FLEETLY_PUB_<NAME>_URL vs
// _BOOTSTRAP/_MECHANISM）。
type ConnMaterialSpec struct {
	// URL 是 nats 方言串（nats://<user>:<pw>@<host>:4222；kafka 空）。
	URL string
	// Bootstrap 是 kafka 方言的 host:port（nats 空）。
	Bootstrap string
	// Mechanism 是 kafka 方言的 SASL 机制名（nats 空）。
	Mechanism string
	// User 是平台合成用户名（双方言同填——NATSPlatformUser/KafkaPlatformUser）。
	User string
	// Password 是机密（永不回显；回读面 PasswordFromURL 的产出）。
	Password string
}

// RenderConnMaterial 铸连接材料方言串（Secret 值单源：nats → URL 形；
// kafka → bootstrap/mechanism/user/password k=v 串——分派判据 = Bootstrap
// 在场即 kafka 方言，bucketstoretemplate.RenderConnMaterial 同款确定性序列化）。
func RenderConnMaterial(m ConnMaterialSpec) string {
	if m.Bootstrap != "" {
		return renderKafkaConnMaterial(m)
	}
	return m.URL
}

// ParseConnMaterial 从方言串回读结构面（写读同源律：RenderConnMaterial
// 铸的两种形态在此解析——含 "://" 即 URL 方言（url.Parse + user/password
// 提取），否则 k=v 方言（kafka 四键严格回读）。坏形态精确失败。
func ParseConnMaterial(material string) (ConnMaterialSpec, error) {
	if !strings.Contains(material, "://") {
		spec, err := parseKafkaConnMaterial(material)
		if err != nil {
			return ConnMaterialSpec{}, err
		}
		return spec, nil
	}
	u, err := url.Parse(material)
	if err != nil {
		return ConnMaterialSpec{}, fmt.Errorf("parse pubsub connection url: %w", err)
	}
	if u.User == nil {
		return ConnMaterialSpec{}, fmt.Errorf("pubsub connection url carries no credentials")
	}
	password, _ := u.User.Password()
	if password == "" {
		return ConnMaterialSpec{}, fmt.Errorf("pubsub connection url carries no password")
	}
	return ConnMaterialSpec{URL: material, User: u.User.Username(), Password: password}, nil
}

// Template 是一个 PubSub 引擎的完整模板知识（per-engine adapter 的接口面；
// B6a 扩面：ConnMaterial 结构面取代 URL 假设、Workload 携 host/资源面、
// Dialect/DataCapabilities 引擎级分叉——kafka 方言基座（KafkaDialect）是
// 连接/注入形态的复用单元，B6b redpanda 嵌入承接）。注册表存目录原型；
// 一切渲染面经 For 绑版后使用。
type Template interface {
	// Engine 返回引擎名（值域键；Workload.Process 同名——engine = 实现名，
	// 不造合成名，design §4.6 方言族机制）。
	Engine() string
	// Dialect 返回方言族词面（目录 dialect 轴：kafka+redpanda 同族互示兼
	// 容；单实现引擎自名——nats/nats）。
	Dialect() string
	// Versions 返回在册版本矩阵（声明序旧→新；每引擎恰一 Default）。
	Versions() []dataservicetemplate.VersionSpec
	// Meta 返回回显与 spec 组装共用的版本/端口（绑版实例按 version 分叉）。
	Meta() Info
	// Image 返回钉版镜像引用（tag@sha256 双段；绑版实例按 version 分叉）。
	Image() string
	// ToolUser 返回工具容器运行用户声明（B8-fix，缺省空 = 镜像自带用户
	// ——dataservicetemplate.ToolUserDefault 嵌入即得；kafka/redpanda 覆盖声明
	// "root"：非 root 镜像用户对 root 属主新卷根与 0600/0400 材料无权
	// 读写。标记门/热应用/轮换/Topic 收敛的工具面经此通道 root 化；实例
	// 载体不受影响）。
	ToolUser() string
	// DataTarget 返回数据卷容器内挂载目标。
	DataTarget() string
	// Knobs 返回该引擎在册的旋钮白名单（键 = 引擎原生词）。
	Knobs() []dataservicetemplate.Knob
	// OwnedKeys 返回平台 owned 键闭集（旋钮守卫对照集）。
	OwnedKeys() []string
	// DataCapabilities 返回引擎级数据面能力位（B6a per-engine 拆分：
	// 族级 Kind descriptor 是缺省，引擎级覆盖——kafka Backup=false 时
	// schedulePass 不铸行、TriggerBackup 拒、目录如实；诚实推论
	// backup=false ⇒ migrate=false）。
	DataCapabilities() dataservicetemplate.DataCapabilities
	// Workload 渲染 Workload 侧参数（env/command；static 旋钮喂入律同
	// databasetemplate——调用方预过滤 staticOnly，纵深二次收敛）。host 是网内
	// DNS 名（kafka advertised.listeners 需要——nats 忽略）；memoryMB 是
	// 载体内存上限 MiB（0 = 无限制——kafka heap 联动的渲染半边，红队
	// F-13：仅设 memory → 默认 heap=memory×0.5）。
	Workload(host string, memoryMB int64, staticSettings map[string]string) (env map[string]string, command []string, err error)
	// Probe 渲染引擎原生存活探针（exec 形态）。
	Probe() []string
	// Materials 渲染 Workload 的凭证材料与 static 旋钮折叠面（密码过
	// 渲染安全闸；空 settings 的产物与无旋钮现状逐字节一致）。memoryMB
	// 是载体内存上限 MiB（0 = 无限制——B8-fix 起 redpanda 在此面渲染
	// rpk.memory：rpk 的 cgroup 自动探测在受限容器里自留 ~58MiB/shard
	// 即崩〔live 实测 2GiB 限 crash-loop〕，显式值是唯一可靠通道；nats/
	// kafka 忽略〔kafka heap 走 Workload env 面〕）。
	Materials(password string, memoryMB int64, staticSettings map[string]string) (map[string][]byte, error)
	// ConnMaterial 铸连接材料结构面（pubsub 类型的连接面单源；host 由
	// 调用方注入——铸名公式真源在 engine/contract；Secret 值 = RenderConnMaterial）。
	ConnMaterial(host, password string) ConnMaterialSpec
	// Backup 渲染单流备份执行（数据库类型契约面）。逐流引擎（nats）结构性
	// 不适用——返回 ErrStreamScopedBackup 哨兵，消费面走 StreamDumpFace；
	// 无备份面引擎（kafka）返回 ErrNoDataBackupFace（挂账）。
	Backup(host, password string) (dataservicetemplate.BackupSpec, error)
	// Restore 渲染恢复执行（同上：逐流引擎返回 ErrStreamScopedRestore；
	// 无备份面引擎返回 ErrNoDataBackupFace）。
	Restore(host, password string) (dataservicetemplate.RestoreSpec, error)
	// RotatePassword 渲染凭证轮换执行（Argv 空 = 声明式轮换——conf 材料
	// 内改密，Secret 重写 + 载体重下发即生效；nats/redis 同款；Argv 非空
	// = 数据面改密——kafka SCRAM user alter，PG 同序）。
	RotatePassword(host, current, next string) (dataservicetemplate.RotateSpec, error)
	// ApplyDynamicSettings 渲染 dynamic 旋钮热应用（无 dynamic 面的引擎
	// 返回 ErrNoDynamicFace 哨兵；kafka = kafka-configs.sh 动态面）。
	ApplyDynamicSettings(host, password string, settings map[string]string) (dataservicetemplate.ApplySpec, error)
	// ImageDigest 是镜像 digest 钉定面（与 Image() 的 digest 段恒一致）。
	ImageDigest() string
}

// LinkageValidator 是 settings×资源面联动校验的可选面（B6a，红队 F-13：
// kafka heap_mb ≤ memory×0.7 的受理半边——Create/Update 受理位在
// ValidateSettings 之外复核跨面约束；无联动面的引擎不实现）。
type LinkageValidator interface {
	ValidateLinkage(memoryMB int64, settings map[string]string) error
}

// StreamDumpFace 是逐流转储方言面（object_set 形态引擎的可选实现；nats
// 首个——ArtifactShape=object_set 的执行契约，design §4.5）。backups 卫星
// 经本面枚举流清单（拒 memory 流）→ 逐流备份（文件产物经 UtilityOutputDir
// 通道回读）→ 逐流恢复（多成员输入注入）。
type StreamDumpFace interface {
	// ListStreams 渲染流清单枚举（stdout = JSON 流名数组）。
	ListStreams(host, password string) (dataservicetemplate.BackupSpec, error)
	// InspectStream 渲染单流探测（stdout = 流信息 JSON——storage 类型判据）。
	InspectStream(host, password, stream string) (dataservicetemplate.BackupSpec, error)
	// DumpStream 渲染单流备份（产物 <outDir>/<stream>.tgz 经输出目录通道
	// 回读——NATS 不可流式，红队 A2①）。
	DumpStream(host, password, stream, outDir string) (dataservicetemplate.BackupSpec, error)
	// RestoreStream 渲染单流恢复（输入 <inDir>/<stream>.tgz；空流前提）。
	RestoreStream(host, password, stream, inDir string) (dataservicetemplate.RestoreSpec, error)
}

// DumpImageFace 是逐流引擎的工具容器镜像面（nats-box 钉版——与引擎载体
// 镜像分立）。
type DumpImageFace interface {
	// NatsBoxImage 同名函数是本面的 nats 实现（见 nats.go）。
	DumpImage() string
}

// DumpImage 返回 nats 工具容器钉版镜像（nats-box；逐流执行面）。
func (natsTemplate) DumpImage() string { return NatsBoxImage() }

// versionBinder 是注册表原型的绑版出厂面（包内协议：For 消费）。
type versionBinder interface {
	bindVersion(spec dataservicetemplate.VersionSpec) Template
}

// —— dataservicetemplate 内核的零漂移再导出（databasetemplate 同款形态） ——

type (
	BackupSpec  = dataservicetemplate.BackupSpec
	RestoreSpec = dataservicetemplate.RestoreSpec
	RotateSpec  = dataservicetemplate.RotateSpec
	ApplySpec   = dataservicetemplate.ApplySpec
	RestoreMode = dataservicetemplate.RestoreMode
)

// ErrNoDynamicFace 是「该引擎无 dynamic 面」哨兵的再导出（内核真源
// dataservicetemplate）。
var ErrNoDynamicFace = dataservicetemplate.ErrNoDynamicFace

var (
	// MintPassword 铸平台随机密码（内核单源；创建面与轮换面共用）。
	MintPassword = dataservicetemplate.MintPassword
	// validatePassword 是渲染闸的包内别名（真源 dataservicetemplate.
	// ValidatePassword）。
	validatePassword = dataservicetemplate.ValidatePassword
	// pinnedRef 是 digest 钉定拼装的包内别名（真源 dataservicetemplate.PinnedRef）。
	pinnedRef = dataservicetemplate.PinnedRef
)

// Kind 是 PubSub 类型的 kind descriptor（ADR-0096 决策 1/8；design
// §4.1）：本包是类型声明层（L2），包初始化期登记——声明与登记一体（var
// 初始化式）。Quota=8（ADR-0096 决策 8 / design §7.4：pubsubs 8）；事件码
// 前缀 pubsub.* 四件（created/updated/deleted/credentials_rotated）；
// DataCapabilities.Backup=true（类型级缺省——nats 逐流备份面在册；引擎级
// 覆盖自 B6a 起下沉 Template.DataCapabilities：kafka false
// 〔EngineBacksUp/schedulePass/TriggerBackup/目录全按引擎级判据〕）。
var Kind = dataservicetemplate.RegisterKind(dataservicetemplate.KindDescriptor{
	Kind:        dataservicetemplate.KindPubSub,
	Quota:       8,
	EventPrefix: "pubsub",
	DataCapabilities: dataservicetemplate.DataCapabilities{
		Backup: true,
	},
})

// InstanceKind 是 pubsub 类型的卷实例标记 kind 词面（ADR-0097 实体
// 中性内核的类型消费锚——与 kind descriptor 键同源）。
const InstanceKind = dataservicetemplate.KindPubSub

// registry 是值域注册表（实现集即值域）。
var registry = map[string]Template{
	"nats":     natsTemplate{},
	"kafka":    kafkaTemplate{KafkaDialect: KafkaDialect{Port: 9092}},
	"redpanda": redpandaTemplate{KafkaDialect: KafkaDialect{Port: 9092}},
}

// EngineBacksUp 报告引擎级数据备份面在场（B6a per-engine 能力位：族门
// KindSupportsBackup 之外的行级判据——schedulePass 逐行过滤与 TriggerBackup
// 受理门消费；模板离册 = false，保守）。kafka false（挂账——无官方 dump
// 工具）；nats true（逐流 object_set 面在册）。
func EngineBacksUp(engine string) bool {
	tpl, err := For(engine, "")
	return err == nil && tpl.DataCapabilities().Backup
}

// Engines 返回在册引擎值域（排序稳定）。
func Engines() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 版本解析的可区分错误（受理位消费：E_INVALID_ARGUMENT 文案列合法值）。
var (
	// ErrUnknownEngine：engine 不在注册表值域。
	ErrUnknownEngine = errors.New("pubsubtemplate: unknown pubsub engine")
	// ErrUnknownVersion：version 不在该引擎的在册矩阵。
	ErrUnknownVersion = errors.New("pubsubtemplate: unknown engine version")
	// ErrVersionDeprecated：受理位弃用门哨兵。
	ErrVersionDeprecated = errors.New("pubsubtemplate: engine version is deprecated")
)

// For 返回 engine 绑定 version 的模板实例（version 空 = Default；行解析
// 放行弃用版本——弃用门只在创建受理位，databasetemplate 同律）。
func For(engine, version string) (Template, error) {
	proto, ok := registry[engine]
	if !ok {
		return nil, fmt.Errorf("%w: %q (available engines: %v)", ErrUnknownEngine, engine, Engines())
	}
	spec, err := ResolveVersion(engine, version)
	if err != nil {
		return nil, err
	}
	return proto.(versionBinder).bindVersion(spec), nil
}

// ResolveVersion 返回 engine 的在册版本条目（空 version = Default）。
func ResolveVersion(engine, version string) (dataservicetemplate.VersionSpec, error) {
	tpl, ok := registry[engine]
	if !ok {
		return dataservicetemplate.VersionSpec{}, fmt.Errorf("%w: %q (available engines: %v)", ErrUnknownEngine, engine, Engines())
	}
	return resolveVersionSpec(engine, tpl.Versions(), version)
}

// AdmitCreateVersion 是创建受理位的弃用门纯核（databasetemplate 同律：弃用拒
// 全新建、同版本带 restore 放行——灾备义）。
func AdmitCreateVersion(engine string, spec dataservicetemplate.VersionSpec, legal []string, hasRestore bool) error {
	if !spec.Deprecated || hasRestore {
		return nil
	}
	note := ""
	if spec.Note != "" {
		note = " (" + spec.Note + ")"
	}
	return fmt.Errorf("%w: %s %q is deprecated%s (available: %v); deprecated versions refuse fresh creates - restore into a current version, or recreate the same version from a backup of it (disaster recovery)",
		ErrVersionDeprecated, engine, spec.ID, note, legal)
}

// RestoreCompat 判定同引擎 from→to 的恢复兼容（数值只升不降——nats 2.10
// →2.11 放行、反向拒；空 from 由调用方先行分流）。
func RestoreCompat(engine, from, to string) error {
	if _, ok := registry[engine]; !ok {
		return fmt.Errorf("%w: %q (available engines: %v)", ErrUnknownEngine, engine, Engines())
	}
	if from == to {
		return nil
	}
	return numericOnlyUp(from, to)
}

// numericOnlyUp 是默认恢复兼容规则（databasetemplate 同形：点分数值逐段比较，
// 降级拒）。
func numericOnlyUp(from, to string) error {
	fv, err := parseNumericVersion(from)
	if err != nil {
		return err
	}
	tv, err := parseNumericVersion(to)
	if err != nil {
		return err
	}
	if compareSegments(tv, fv) < 0 {
		return fmt.Errorf("pubsubtemplate: restore is a version downgrade (%s -> %s); engine versions only upgrade - create the target on the backup version or newer", from, to)
	}
	return nil
}

func compareSegments(a, b []int) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}

func parseNumericVersion(v string) ([]int, error) {
	if v == "" {
		return nil, fmt.Errorf("pubsubtemplate: %q is not a numeric engine version id", v)
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || p == "" {
			return nil, fmt.Errorf("pubsubtemplate: %q is not a numeric engine version id", v)
		}
		out = append(out, n)
	}
	return out, nil
}

// VersionsFor 返回 engine 的在册版本矩阵（目录装配消费；不存在 ok=false）。
func VersionsFor(engine string) ([]dataservicetemplate.VersionSpec, bool) {
	tpl, ok := registry[engine]
	if !ok {
		return nil, false
	}
	return tpl.Versions(), true
}

// VersionIDs 报告 specs 的版本 ID 集（错误文案与目录面共用；保序）。
func VersionIDs(specs []dataservicetemplate.VersionSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.ID
	}
	return out
}

func resolveVersionSpec(engineName string, specs []dataservicetemplate.VersionSpec, version string) (dataservicetemplate.VersionSpec, error) {
	if version == "" {
		for _, s := range specs {
			if s.Default {
				return s, nil
			}
		}
		return dataservicetemplate.VersionSpec{}, fmt.Errorf(
			"pubsubtemplate: engine %q carries no default version (registry invariant broken)", engineName)
	}
	for _, s := range specs {
		if s.ID == version {
			return s, nil
		}
	}
	return dataservicetemplate.VersionSpec{}, fmt.Errorf("%w: %s %q (available: %v)",
		ErrUnknownVersion, engineName, version, VersionIDs(specs))
}

// InfoFor 返回 engine 的回显信息（按 Default 版本绑定；port 不随版本分叉）。
func InfoFor(engine string) (Info, bool) {
	tpl, err := For(engine, "")
	if err != nil {
		return Info{}, false
	}
	return tpl.Meta(), true
}

// PasswordFromURL 从连接材料方言串提取密码（写读同源律的密码半边；B6a
// 起双方言：nats URL 形与 kafka k=v 形都经 ParseConnMaterial 回读——名
// 保留 URL 是 B4b 契约先例，回读范围以实现为准）。
func PasswordFromURL(connMaterial string) (string, error) {
	spec, err := ParseConnMaterial(connMaterial)
	if err != nil {
		return "", err
	}
	return spec.Password, nil
}
