// Package buckettemplate 是 Bucket Store 家族模板注册表（B5，ADR-0096 决策
// 1/3/4/7；design §4.1 L2 家族/引擎声明层）：engine 名 → 版本矩阵
// （Versions）+ 端口 + 数据卷目标 + 凭证渲染方式。形状与 pubtemplate 同构
// （enginetemplate 共享内核的家族消费面）；本批 silo 单实现（S3 方言族——
// 未来 RustFS 类兼容实现同机制承接，ADR-0042 挂账不吸收）。
//
// 凭证/连接方言（ADR-0096 决策 3/4；design §4.4）：复合凭证 AK/SK——
// ConnMaterial = `endpoint=<host:port>;access_key=<ak>;secret_key=<sk>`
// k=v 串（S3 系无 URL 方言：endpoint + 两段凭证）；Secret 值 = 该方言串，
// 渲染器/回读器同源（ConnMaterial 铸形 ↔ ParseConnMaterial 回读，写读同源
// 律）。凭证名 `bucketstore:<name>`（database:/pubsub: 同先例）。AK 是平台
// 合成用户名（NATSPlatformUser 同款单真源——S3 语义里 access key 非机密、
// 只有 secret key 是机密）；SK 由 MintPassword 铸。
//
// 凭证下发形态（ADR-0014 附录 A.2 追认，ADR-0096 决策 5）：silo 系 MinIO
// fork，root 凭证只走启动 env（MINIO_ROOT_USER/MINIO_ROOT_PASSWORD）——
// **长效凭证随载体 spec env 下发**，暴露面大于住材料文件的 DB 密码/nats
// conf；接受论证 = 同信任域（载体 spec 只进 Runtime.Ensure，不进用户可见
// Spec/Revision/日志）+ 单 Project 独占实例 + 轮换=滚动补偿（SK 进 Workload
// env → 投影指纹 → Generation 推进 → stop-first 滚动；旧值在新载体上线前
// 保持有效，无破窗）。单用户模式取舍（评审 F-07）：root 全权限是 zot
// per-Project 凭证域先例的倒退；v1 接受 = 单 Project 独占 + per-App service
// account 挂账（IAM 策略面可支撑，live 定案留后手）。
//
// 转储契约（design §4.5 L4 家族特化表）：silo **v1 挂账**——数据冗余/外同步
// 域非 dump 契约对应物；Family.DataCapabilities.Backup=false（目录
// capabilities 如实；backups 卫星不铸行、无 TriggerBackup 面、Create 无
// restore_from_backup）。能力位诚实推论 backup=false ⇒ migrate=false。
//
// 本包 stdlib-only 近叶子（除 enginetemplate 内核外无 internal 依赖——
// import 守卫同 pubtemplate 口径）；镜像 digest 钉定门禁随本包落
// （TestImageDigestsPinned 同构）。
package buckettemplate

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// Info 是 API 回显面的模板信息（version/port；与 pubtemplate.Info 同形）。
type Info struct {
	Version string
	Port    int32
}

// Template 是一个 Bucket Store 引擎的完整模板知识（per-engine adapter 的
// 接口面；形状按 enginetemplate 消费面直接实现——pubtemplate 同构，差异面
// 收在：凭证是 AK/SK 复合、Workload 携带凭证 env、无 Dump/Load 面）。注册
// 表存目录原型；一切渲染面经 For 绑版后使用。
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
	// Knobs 返回该引擎在册的旋钮白名单（键 = 引擎原生词；silo v1 零旋钮）。
	Knobs() []enginetemplate.Knob
	// OwnedKeys 返回平台 owned 键闭集（旋钮守卫对照集）。
	OwnedKeys() []string
	// Workload 渲染 Workload 侧参数（env/command）。凭证经 env 下发（ADR-0014
	// A.2 追认，包注）；staticSettings 是喂入律输入（本族 v1 零旋钮，签名
	// 带参保持接口齐形——dynamic 键永不进 env）。
	Workload(accessKey, secretKey string, staticSettings map[string]string) (env map[string]string, command []string, err error)
	// Probe 渲染引擎原生存活探针（exec 形态）。
	Probe() []string
	// Materials 渲染 Workload 的文件材料面（silo 凭证住 env、无 conf 文件
	// ——恒空 map；接口齐形保留给带 conf 面的未来引擎）。
	Materials(staticSettings map[string]string) (map[string][]byte, error)
	// ConnMaterial 铸连接材料方言串（bucketstore 家族的连接面单源；host
	// 由调用方注入——铸名公式真源在 engineports）。
	ConnMaterial(host, accessKey, secretKey string) string
	// RotatePassword 渲染凭证轮换执行（Argv 空 = 声明式轮换——SK 进 Workload
	// env，Secret 重写 + 投影指纹变 + Generation 推进 = 滚动生效）。
	RotatePassword(host, current, next string) (enginetemplate.RotateSpec, error)
	// ApplyDynamicSettings 渲染 dynamic 旋钮热应用（无 dynamic 面的引擎
	// 返回 ErrNoDynamicFace 哨兵）。
	ApplyDynamicSettings(host, secretKey string, settings map[string]string) (enginetemplate.ApplySpec, error)
	// ImageDigest 是镜像 digest 钉定面（与 Image() 的 digest 段恒一致）。
	ImageDigest() string
}

// versionBinder 是注册表原型的绑版出厂面（包内协议：For 消费）。
type versionBinder interface {
	bindVersion(spec enginetemplate.VersionSpec) Template
}

// —— enginetemplate 内核的零漂移再导出（pubtemplate 同款形态） ——

type (
	RotateSpec = enginetemplate.RotateSpec
	ApplySpec  = enginetemplate.ApplySpec
)

// ErrNoDynamicFace 是「该引擎无 dynamic 面」哨兵的再导出（内核真源
// enginetemplate）。
var ErrNoDynamicFace = enginetemplate.ErrNoDynamicFace

var (
	// MintPassword 铸平台随机 secret key（内核单源；创建面与轮换面共用）。
	MintPassword = enginetemplate.MintPassword
	// validatePassword 是渲染闸的包内别名（真源 enginetemplate.
	// ValidatePassword）。
	validatePassword = enginetemplate.ValidatePassword
	// pinnedRef 是 digest 钉定拼装的包内别名（真源 enginetemplate.PinnedRef）。
	pinnedRef = enginetemplate.PinnedRef
)

// Family 是 Bucket Store 家族的 family descriptor（ADR-0096 决策 1/8；design
// §4.1）：本包是家族声明层（L2），包初始化期登记——声明与登记一体（var
// 初始化式）。Quota=4（ADR-0096 决策 8 / design §7.4：bucket_stores 4）；
// 事件码族 bucket_store.* 四件（created/updated/deleted/credentials_rotated
// ——EventFamily 与家族词 bucketstore 不同形，descriptor 独立成员的动机）；
// DataCapabilities.Backup=false（silo 转储挂账——design §4.5 L4 表；backups
// 卫星按此不铸行，红队 A9）。
var Family = enginetemplate.RegisterFamily(enginetemplate.FamilyDescriptor{
	Family:      enginetemplate.FamilyBucketStore,
	Quota:       4,
	EventFamily: "bucket_store",
	DataCapabilities: enginetemplate.DataCapabilities{
		Backup: false,
	},
})

// InstanceFamily 是 bucketstore 家族的卷实例标记 family 词面（ADR-0097 实体
// 中性内核的家族消费锚——与 family descriptor 键同源）。
const InstanceFamily = enginetemplate.FamilyBucketStore

// registry 是值域注册表（实现集即值域）。
var registry = map[string]Template{
	"silo": siloTemplate{},
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
	ErrUnknownEngine = errors.New("buckettemplate: unknown bucket store engine")
	// ErrUnknownVersion：version 不在该引擎的在册矩阵。
	ErrUnknownVersion = errors.New("buckettemplate: unknown engine version")
	// ErrVersionDeprecated：受理位弃用门哨兵。
	ErrVersionDeprecated = errors.New("buckettemplate: engine version is deprecated")
)

// For 返回 engine 绑定 version 的模板实例（version 空 = Default；行解析
// 放行弃用版本——弃用门只在创建受理位，pubtemplate 同律）。
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

// AdmitCreateVersion 是创建受理位的弃用门纯核（pubtemplate 同律：弃用拒
// 新建。本族无备份面故无「同版本带 restore 放行」分支——灾备义在本族
// 不成立，弃用版一律拒建）。
func AdmitCreateVersion(engine string, spec enginetemplate.VersionSpec, legal []string) error {
	if !spec.Deprecated {
		return nil
	}
	note := ""
	if spec.Note != "" {
		note = " (" + spec.Note + ")"
	}
	return fmt.Errorf("%w: %s %q is deprecated%s (available: %v); deprecated versions refuse fresh creates - create on a current version",
		ErrVersionDeprecated, engine, spec.ID, note, legal)
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
			"buckettemplate: engine %q carries no default version (registry invariant broken)", engineName)
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

// SplitSettings 把 settings 按旋钮 class 分流为 staticOnly/dynamicOnly
// （ADR-0095 决策 4 喂入律帮手——bucketstore 家族消费面；silo v1 零旋钮，
// 两集恒空，接口齐形保留给带旋钮的未来引擎）。
func SplitSettings(knobs []enginetemplate.Knob, settings map[string]string) (staticOnly, dynamicOnly map[string]string) {
	staticOnly, dynamicOnly = map[string]string{}, map[string]string{}
	byKey := make(map[string]enginetemplate.Knob, len(knobs))
	for _, k := range knobs {
		byKey[k.Key] = k
	}
	for key, value := range settings {
		knob, ok := byKey[key]
		if !ok {
			continue
		}
		switch knob.Class {
		case enginetemplate.KnobStatic:
			staticOnly[key] = value
		case enginetemplate.KnobDynamic:
			dynamicOnly[key] = value
		}
	}
	return staticOnly, dynamicOnly
}

// —— 连接材料方言（AK/SK 复合凭证 k=v 串；写读同源律） ——

// 方言串的键名（渲染器与回读器共用的字面量单源；`endpoint`/`access_key`/
// `secret_key` 三键，分号分隔、等号赋值——S3 客户端配置的常见键名词面）。
const (
	connKeyEndpoint = "endpoint"
	connKeyAccess   = "access" + "_key"
	connKeySecret   = "secret" + "_key"
)

// ConnMaterial 是解析后的连接材料（回读面产物）。
type ConnMaterial struct {
	// Endpoint 是 host:port（网内 DNS 名 + S3 API 口；无 scheme——明文
	// HTTP 内网口径，客户端自配 scheme/TLS 关）。
	Endpoint string
	// AccessKey 是 S3 access key（平台合成用户名；非机密）。
	AccessKey string
	// SecretKey 是 S3 secret key（机密；永不回显）。
	SecretKey string
}

// RenderConnMaterial 铸方言串（ConnMaterial 的逆：k=v 分号串，键序固定
// endpoint → access_key → secret_key——确定性序列化）。
func RenderConnMaterial(m ConnMaterial) string {
	return connKeyEndpoint + "=" + m.Endpoint + ";" +
		connKeyAccess + "=" + m.AccessKey + ";" +
		connKeySecret + "=" + m.SecretKey
}

// ParseConnMaterial 从连接材料方言串回读三段（写读同源律：RenderConnMaterial
// 铸的 k=v 串在此解析——键缺席/空值/未知键都精确失败）。
func ParseConnMaterial(material string) (ConnMaterial, error) {
	var out ConnMaterial
	seen := map[string]bool{}
	for _, pair := range strings.Split(material, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return ConnMaterial{}, fmt.Errorf("bucket store connection material: segment %q is not key=value", pair)
		}
		if seen[key] {
			return ConnMaterial{}, fmt.Errorf("bucket store connection material: key %q appears twice", key)
		}
		seen[key] = true
		switch key {
		case connKeyEndpoint:
			out.Endpoint = value
		case connKeyAccess:
			out.AccessKey = value
		case connKeySecret:
			out.SecretKey = value
		default:
			return ConnMaterial{}, fmt.Errorf("bucket store connection material: unknown key %q", key)
		}
	}
	if out.Endpoint == "" {
		return ConnMaterial{}, fmt.Errorf("bucket store connection material carries no endpoint")
	}
	if out.AccessKey == "" {
		return ConnMaterial{}, fmt.Errorf("bucket store connection material carries no access key")
	}
	if out.SecretKey == "" {
		return ConnMaterial{}, fmt.Errorf("bucket store connection material carries no secret key")
	}
	return out, nil
}
