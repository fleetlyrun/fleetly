// Package pubtemplate 是 PubSub 家族模板注册表（B4b，ADR-0096 决策 1/3/7；
// design §4.1 L2 家族/引擎声明层）：engine 名 → 版本矩阵（Versions）+ 端口 +
// 数据卷目标 + 凭证材料渲染方式。形状与 dbtemplate 同构（enginetemplate
// 共享内核的家族消费面）；**方言基座本批不建**（kafka/redpanda 方言族 B6
// 接入时再立——本批 nats 单实现，Template 接口按 enginetemplate 形状直接
// 实现）。
//
// 凭证/连接方言（ADR-0096 决策 3/4）：连接材料渲染取代 URL 假设——nats
// 出 ConnMaterial = `nats://<user>:<pw>@<host>:4222`（URL 形）；kafka 的
// bootstrap+SASL 方言 B6。Secret 值 = 家族方言串，渲染器/回读器同源
// （ConnURL 铸形 ↔ PasswordFromURL 回读，写读同源律）。凭证名
// `pubsub:<name>`（与 database:<name> 同先例）。
//
// 本包 stdlib-only 近叶子（除 enginetemplate 内核外无 internal 依赖——
// import 守卫同 dbtemplate 口径）；镜像 digest 钉定门禁
// （TestImageDigestsPinned/TestTemplateFaces）随 B4b 扩本包。
package pubtemplate

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// Info 是 API 回显面的模板信息（version/port；与 dbtemplate.Info 同形）。
type Info struct {
	Version string
	Port    int32
}

// Template 是一个 PubSub 引擎的完整模板知识（per-engine adapter 的接口面；
// 形状按 enginetemplate 消费面直接实现——B4b 不立方言基座，kafka 方言族
// B6 以嵌入基座承接时本接口是基座形状的真源）。注册表存目录原型；一切
// 渲染面经 For 绑版后使用。
type Template interface {
	// Engine 返回引擎名（值域键；Workload.Process 同名）。
	Engine() string
	// Versions 返回在册版本矩阵（声明序旧→新；每引擎恰一 Default）。
	Versions() []enginetemplate.VersionSpec
	// Meta 返回回显与 spec 组装共用的版本/端口（绑版实例按 version 分叉）。
	Meta() Info
	// Image 返回钉版镜像引用（tag@sha256 双段；绑版实例按 version 分叉）。
	Image() string
	// DataTarget 返回数据卷容器内挂载目标。
	DataTarget() string
	// Knobs 返回该引擎在册的旋钮白名单（键 = 引擎原生词）。
	Knobs() []enginetemplate.Knob
	// OwnedKeys 返回平台 owned 键闭集（旋钮守卫对照集）。
	OwnedKeys() []string
	// Workload 渲染 Workload 侧参数（env/command；static 旋钮喂入律同
	// dbtemplate——本家族首批旋钮全 static 住材料面）。
	Workload(staticSettings map[string]string) (env map[string]string, command []string)
	// Probe 渲染引擎原生存活探针（exec 形态）。
	Probe() []string
	// Materials 渲染 Workload 的凭证材料与 static 旋钮折叠面（密码过
	// 渲染安全闸；空 settings 的产物与无旋钮现状逐字节一致）。
	Materials(password string, staticSettings map[string]string) (map[string][]byte, error)
	// ConnURL 铸连接材料方言串（pubsub 家族的连接面单源；host 由调用方
	// 注入——铸名公式真源在 engineports）。
	ConnURL(host, password string) string
	// Backup 渲染单流备份执行（数据库家族契约面）。逐流引擎（nats）结构性
	// 不适用——返回 ErrStreamScopedBackup 哨兵，消费面走 StreamDumpFace。
	Backup(host, password string) (enginetemplate.BackupSpec, error)
	// Restore 渲染恢复执行（同上：逐流引擎返回 ErrStreamScopedRestore）。
	Restore(host, password string) (enginetemplate.RestoreSpec, error)
	// RotatePassword 渲染凭证轮换执行（Argv 空 = 声明式轮换——conf 材料
	// 内改密，Secret 重写 + 载体重下发即生效；nats/redis 同款）。
	RotatePassword(host, current, next string) (enginetemplate.RotateSpec, error)
	// ApplyDynamicSettings 渲染 dynamic 旋钮热应用（无 dynamic 面的引擎
	// 返回 ErrNoDynamicFace 哨兵）。
	ApplyDynamicSettings(host, password string, settings map[string]string) (enginetemplate.ApplySpec, error)
	// ImageDigest 是镜像 digest 钉定面（与 Image() 的 digest 段恒一致）。
	ImageDigest() string
}

// StreamDumpFace 是逐流转储方言面（object_set 形态引擎的可选实现；nats
// 首个——ArtifactShape=object_set 的执行契约，design §4.5）。backups 卫星
// 经本面枚举流清单（拒 memory 流）→ 逐流备份（文件产物经 UtilityOutputDir
// 通道回读）→ 逐流恢复（多成员输入注入）。
type StreamDumpFace interface {
	// ListStreams 渲染流清单枚举（stdout = JSON 流名数组）。
	ListStreams(host, password string) (enginetemplate.BackupSpec, error)
	// InspectStream 渲染单流探测（stdout = 流信息 JSON——storage 类型判据）。
	InspectStream(host, password, stream string) (enginetemplate.BackupSpec, error)
	// DumpStream 渲染单流备份（产物 <outDir>/<stream>.tgz 经输出目录通道
	// 回读——NATS 不可流式，红队 A2①）。
	DumpStream(host, password, stream, outDir string) (enginetemplate.BackupSpec, error)
	// RestoreStream 渲染单流恢复（输入 <inDir>/<stream>.tgz；空流前提）。
	RestoreStream(host, password, stream, inDir string) (enginetemplate.RestoreSpec, error)
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
	bindVersion(spec enginetemplate.VersionSpec) Template
}

// —— enginetemplate 内核的零漂移再导出（dbtemplate 同款形态） ——

type (
	BackupSpec  = enginetemplate.BackupSpec
	RestoreSpec = enginetemplate.RestoreSpec
	RotateSpec  = enginetemplate.RotateSpec
	ApplySpec   = enginetemplate.ApplySpec
	RestoreMode = enginetemplate.RestoreMode
)

// ErrNoDynamicFace 是「该引擎无 dynamic 面」哨兵的再导出（内核真源
// enginetemplate）。
var ErrNoDynamicFace = enginetemplate.ErrNoDynamicFace

var (
	// MintPassword 铸平台随机密码（内核单源；创建面与轮换面共用）。
	MintPassword = enginetemplate.MintPassword
	// validatePassword 是渲染闸的包内别名（真源 enginetemplate.
	// ValidatePassword）。
	validatePassword = enginetemplate.ValidatePassword
	// pinnedRef 是 digest 钉定拼装的包内别名（真源 enginetemplate.PinnedRef）。
	pinnedRef = enginetemplate.PinnedRef
)

// Family 是 PubSub 家族的 family descriptor（ADR-0096 决策 1/8；design
// §4.1）：本包是家族声明层（L2），包初始化期登记——声明与登记一体（var
// 初始化式）。Quota=8（ADR-0096 决策 8 / design §7.4：pubsubs 8）；事件码
// 族 pubsub.* 四件（created/updated/deleted/credentials_rotated）；
// DataCapabilities.Backup=true（nats 逐流备份面在册——kafka false 随 B6a
// 的 per-engine 分叉下沉）。
var Family = enginetemplate.RegisterFamily(enginetemplate.FamilyDescriptor{
	Family:      enginetemplate.FamilyPubSub,
	Quota:       8,
	EventFamily: "pubsub",
	DataCapabilities: enginetemplate.DataCapabilities{
		Backup: true,
	},
})

// InstanceFamily 是 pubsub 家族的卷实例标记 family 词面（ADR-0097 实体
// 中性内核的家族消费锚——与 family descriptor 键同源）。
const InstanceFamily = enginetemplate.FamilyPubSub

// registry 是值域注册表（实现集即值域）。
var registry = map[string]Template{
	"nats": natsTemplate{},
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
	ErrUnknownEngine = errors.New("pubtemplate: unknown pubsub engine")
	// ErrUnknownVersion：version 不在该引擎的在册矩阵。
	ErrUnknownVersion = errors.New("pubtemplate: unknown engine version")
	// ErrVersionDeprecated：受理位弃用门哨兵。
	ErrVersionDeprecated = errors.New("pubtemplate: engine version is deprecated")
)

// For 返回 engine 绑定 version 的模板实例（version 空 = Default；行解析
// 放行弃用版本——弃用门只在创建受理位，dbtemplate 同律）。
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
func ResolveVersion(engine, version string) (enginetemplate.VersionSpec, error) {
	tpl, ok := registry[engine]
	if !ok {
		return enginetemplate.VersionSpec{}, fmt.Errorf("%w: %q (available engines: %v)", ErrUnknownEngine, engine, Engines())
	}
	return resolveVersionSpec(engine, tpl.Versions(), version)
}

// AdmitCreateVersion 是创建受理位的弃用门纯核（dbtemplate 同律：弃用拒
// 全新建、同版本带 restore 放行——灾备义）。
func AdmitCreateVersion(engine string, spec enginetemplate.VersionSpec, legal []string, hasRestore bool) error {
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

// numericOnlyUp 是默认恢复兼容规则（dbtemplate 同形：点分数值逐段比较，
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
		return fmt.Errorf("pubtemplate: restore is a version downgrade (%s -> %s); engine versions only upgrade - create the target on the backup version or newer", from, to)
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
		return nil, fmt.Errorf("pubtemplate: %q is not a numeric engine version id", v)
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || p == "" {
			return nil, fmt.Errorf("pubtemplate: %q is not a numeric engine version id", v)
		}
		out = append(out, n)
	}
	return out, nil
}

// VersionsFor 返回 engine 的在册版本矩阵（目录装配消费；不存在 ok=false）。
func VersionsFor(engine string) ([]enginetemplate.VersionSpec, bool) {
	tpl, ok := registry[engine]
	if !ok {
		return nil, false
	}
	return tpl.Versions(), true
}

// VersionIDs 报告 specs 的版本 ID 集（错误文案与目录面共用；保序）。
func VersionIDs(specs []enginetemplate.VersionSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.ID
	}
	return out
}

func resolveVersionSpec(engineName string, specs []enginetemplate.VersionSpec, version string) (enginetemplate.VersionSpec, error) {
	if version == "" {
		for _, s := range specs {
			if s.Default {
				return s, nil
			}
		}
		return enginetemplate.VersionSpec{}, fmt.Errorf(
			"pubtemplate: engine %q carries no default version (registry invariant broken)", engineName)
	}
	for _, s := range specs {
		if s.ID == version {
			return s, nil
		}
	}
	return enginetemplate.VersionSpec{}, fmt.Errorf("%w: %s %q (available: %v)",
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

// PasswordFromURL 从连接材料方言串提取密码（写读同源律：ConnURL 铸的
// URL 形在此解析——nats 方言单源；kafka bootstrap 方言 B6 扩面）。
func PasswordFromURL(connMaterial string) (string, error) {
	u, err := url.Parse(connMaterial)
	if err != nil {
		return "", fmt.Errorf("parse pubsub connection url: %w", err)
	}
	if u.User == nil {
		return "", fmt.Errorf("pubsub connection url carries no credentials")
	}
	password, _ := u.User.Password()
	if password == "" {
		return "", fmt.Errorf("pubsub connection url carries no password")
	}
	return password, nil
}
