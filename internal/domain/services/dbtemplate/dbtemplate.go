// Package dbtemplate 是数据库模板注册表（ADR-0029 决策 2；B2a 版本化改造，
// ADR-0094 决策 2）：engine 名 → 版本矩阵（Versions）+ 端口 + 数据卷目标 +
// 凭证材料渲染方式。跨版本差异收在各引擎 adapter 内（绑版实例持 version
// 字段——For(engine, version) 出厂）；共享机制形状（VersionSpec/转储与
// 轮换 spec/凭证铸造与渲染闸/卷实例标记契约）自本包下沉 enginetemplate
// （B2a，2026-11-05），本包经别名零漂移消费——engineports facts.go 先例。
//
// 形状（2026-10-03，架构评审第二轮候选 1，F2.7 目录化提前落）：per-engine
// 接口 adapter——每引擎一个类型实现 Template，注册表零 switch（加引擎 =
// 加一个类型 + 一个注册表 entry）。F2.7 收口（2026-10-05，ADR-0045）：镜像
// 引用 digest 钉定门禁落地——Image() 恒为 tag@digest 双段形态，钉定对
// 常量住各版本条目（清单视图 = dbtemplate_test 的 TestTemplateFaces 与
// TestImageDigestsPinned 钉板，B2a 起按版本矩阵全量执法）。host 注入：模板
// 只拥有 "这个引擎的 URL 长什么样"，铸名公式（db-<id>）留在 engine
// （TaskDNSName 先例同款分居）。本包 stdlib-only 近叶子（import 守卫口径
// 下除 enginetemplate 内核外无 internal 依赖）。
//
// 钉版调研（2026-10-02）：postgres 取 major+suite 级钉（trixie 基座启动
// 坑 docker-library/postgres#1363 规避；精确 patch 钉随 F2）；pgvector 落
// 上游 pgvector/pgvector（percona/pgvector 镜像不存在于 Docker Hub——
// Percona 把 pgvector 打进其发行镜像，单 arch 且约 700MB，不取）。
package dbtemplate

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// Info 是 API 回显面的模板信息（version/port；不含镜像——能力自描述面由
// Registry/Provider 承载，数据库模板不是 Provider，ADR-0029）。B2a 起
// Version 语义 = 绑版实例的版本回显（VersionSpec.ID——用户面 major 短名，
// 如 "17"/"0.8-pg18"；非镜像 tag）。
type Info struct {
	Version string
	Port    int32
}

// Template 是一个数据库引擎的完整模板知识（per-engine adapter 的接口面；
// 注册表值域即实现集）。注册表存「目录原型」（版本未绑定的 adapter 零值
// ——仅承载目录枚举与绑版出厂）；一切渲染面经 For 绑版后使用。
type Template interface {
	// Engine 返回引擎名（值域键；Workload.Process 同名）。
	Engine() string
	// Versions 返回在册版本矩阵（ADR-0094 决策 2；排序稳定 = 声明序旧→新；
	// 每引擎恰一 Default、Deprecated 语义由门禁钉死）。
	Versions() []enginetemplate.VersionSpec
	// Meta 返回回显与 spec 组装共用的版本/端口（单源；绑版实例按 version
	// 分叉）。
	Meta() Info
	// Image 返回钉版镜像引用（ADR-0021 口径；F2.7/ADR-0045 起 digest 钉定：
	// 恒为 tag@sha256:<index digest> 双段形态，digest 主导、tag 是可读性面；
	// 绑版实例按 version 分叉）。
	Image() string
	// DataTarget 返回数据卷容器内挂载目标。
	DataTarget() string
	// Workload 渲染 Workload 侧参数（env/command——材料文件名是路径不是
	// 值，env 不落明文，ADR-0014；跨版本差异〔如 PGDATA 布局〕在此分叉）。
	Workload() (env map[string]string, command []string)
	// Probe 渲染引擎原生存活探针（exec 形态；镜像必带引擎自带客户端工具，
	// 不依赖通用 TCP 探针方言的 nc 假设——staging 真机实证 pgvector 镜像
	// 无 nc，F1.15）。
	Probe() []string
	// Materials 渲染 DB Workload 的凭证材料（密码经 URL 解析取得——单真源
	// 是 Secret 里的连接串，ADR-0029 决策 6）。密码超出安全字符集返回
	// 错误（渲染面 fail-closed，见 enginetemplate.ValidatePassword）。
	Materials(password string) (map[string][]byte, error)
	// ConnURL 铸连接串（host 由调用方注入——铸名公式真源在 engine）。
	ConnURL(host, password string) string
	// Backup 渲染备份执行（ADR-0039 决策 5）：完整 argv + 凭证材料 +
	// env；dump 产物走工具容器 stdout（执行器流送 ObjectStore）。host 是
	// engine 铸的网内 DNS 名（db-<id>）。
	Backup(host, password string) (enginetemplate.BackupSpec, error)
	// Restore 渲染恢复执行：流式（stdin 注入运行中的库）或预置卷（redis
	// 形态——RDB 仅启动时装载，ADR-0039 决策 5）。
	Restore(host, password string) (enginetemplate.RestoreSpec, error)
	// RotatePassword 渲染凭证轮换执行（IA v3 二期⑤b）：current 供工具
	// 容器认证，next 是目标密码（两者都过渲染闸）。Argv 非空 = 数据面改密
	// 命令（密码活在数据卷内的引擎——postgres/mysql/mongo，一次性工具容器
	// 执行）；Argv 空 = 声明式轮换（redis——requirepass 在平台合成材料内，
	// Secret 重写 + 载体重下发即生效）。
	RotatePassword(host, current, next string) (enginetemplate.RotateSpec, error)
	// ImageDigest 是镜像 digest 钉定面（F2.7/ADR-0045 落地）：带算法前缀的
	// index digest，与 Image() 的 digest 段恒一致（P7 第三消费点 = registry
	// digest 透传，只核对传递完整性——测试门禁断言自洽）。
	ImageDigest() string
}

// versionBinder 是注册表原型的绑版出厂面（包内协议：For 消费——每个
// adapter 实现之，把解析出的 VersionSpec 绑成持版本的实例；跨版本差异
// 轴〔如 pg 系 PGDATA 布局〕随 adapter 本地版本表携带）。
type versionBinder interface {
	bindVersion(spec enginetemplate.VersionSpec) Template
}

// —— enginetemplate 内核下沉的零漂移再导出（B2a，engineports facts.go
// 先例：真源在 internal/domain/services/enginetemplate，dbtemplate 与既有
// 消费面经本面零改消费）——

type (
	BackupSpec     = enginetemplate.BackupSpec
	RestoreSpec    = enginetemplate.RestoreSpec
	RotateSpec     = enginetemplate.RotateSpec
	RestoreMode    = enginetemplate.RestoreMode
	InstanceMarker = enginetemplate.InstanceMarker
)

const (
	RestoreStream      = enginetemplate.RestoreStream
	RestorePreseed     = enginetemplate.RestorePreseed
	SeedMountPoint     = enginetemplate.SeedMountPoint
	BackupInputPath    = enginetemplate.BackupInputPath
	InstanceMarkerFile = enginetemplate.InstanceMarkerFile
	// InstanceFamily 是数据库家族的卷实例标记 family 词面（实体中性内核
	// 的家族消费锚——pubsub/bucketstore 随 B4b/B5 各自带词面）。
	InstanceFamily = "database"
)

var (
	// MintPassword 铸平台随机密码（内核单源；创建面与轮换面共用）。
	MintPassword = enginetemplate.MintPassword
	// validatePassword 是渲染闸的包内别名（真源 enginetemplate.
	// ValidatePassword；adapter 渲染面零改）。
	validatePassword = enginetemplate.ValidatePassword
	// pinnedRef 是 digest 钉定拼装的包内别名（真源 enginetemplate.
	// PinnedRef；adapter 拼装面零改）。
	pinnedRef = enginetemplate.PinnedRef

	ParseInstanceMarker     = enginetemplate.ParseInstanceMarker
	InstanceMarkerClaimArgv = enginetemplate.InstanceMarkerClaimArgv
	InstanceMarkerReadArgv  = enginetemplate.InstanceMarkerReadArgv
)

// MintInstanceMarker 铸数据库家族的卷实例标记（内核签名含 family 轴——
// 本面注入数据库家族词面，ensure 链消费点零改）。
func MintInstanceMarker(databaseID string, now time.Time) enginetemplate.InstanceMarker {
	return enginetemplate.MintInstanceMarker(databaseID, InstanceFamily, now)
}

// registry 是值域注册表（受理位校验与投影共用的单源；实现集即值域）。
var registry = map[string]Template{
	"postgres": postgresTemplate{},
	"pgvector": pgvectorTemplate{},
	"redis":    redisTemplate{},
	"mysql":    mysqlTemplate{},
	"mongo":    mongoTemplate{},
}

// Engines 返回在册引擎值域（受理位校验消费；排序稳定）。
func Engines() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 版本解析的可区分错误（受理位消费：E_INVALID_ARGUMENT 文案列合法值与
// 弃用说明，errcode 零新码——ADR-0094 决策 3）。
var (
	// ErrUnknownEngine：engine 不在注册表值域。
	ErrUnknownEngine = errors.New("dbtemplate: unknown database engine")
	// ErrUnknownVersion：version 不在该引擎的在册矩阵。
	ErrUnknownVersion = errors.New("dbtemplate: unknown engine version")
	// ErrVersionDeprecated：version 在册但已弃用（拒全新建；存量与带恢复
	// 源重建的受理口径随 B2c 受理位细化，ADR-0094 决策 6）。
	ErrVersionDeprecated = errors.New("dbtemplate: engine version is deprecated")
)

// For 返回 engine 绑定 version 的模板实例（ADR-0094 决策 2：adapter 持
// version 字段，跨版本差异收在 adapter 内）。version 空 = Default；
// ErrUnknownEngine/ErrUnknownVersion/ErrVersionDeprecated 三类可区分。
func For(engine, version string) (Template, error) {
	proto, ok := registry[engine]
	if !ok {
		return nil, fmt.Errorf("%w: %q (available engines: %v)", ErrUnknownEngine, engine, Engines())
	}
	spec, err := resolveVersionSpec(engine, proto.Versions(), version)
	if err != nil {
		return nil, err
	}
	return proto.(versionBinder).bindVersion(spec), nil
}

// VersionsFor 返回 engine 的在册版本矩阵（目录装配与校验消费；排序稳定
// = 声明序旧→新；不存在 ok=false）。
func VersionsFor(engine string) ([]enginetemplate.VersionSpec, bool) {
	tpl, ok := registry[engine]
	if !ok {
		return nil, false
	}
	return tpl.Versions(), true
}

// resolveVersionSpec 是版本解析纯核（红灯实验直测，scanEngineNameLiterals
// 先例）：空 version = Default；Deprecated 拒（Note 附带）；未命中列合法值。
func resolveVersionSpec(engineName string, specs []enginetemplate.VersionSpec, version string) (enginetemplate.VersionSpec, error) {
	if version == "" {
		for _, s := range specs {
			if s.Default {
				return s, nil
			}
		}
		return enginetemplate.VersionSpec{}, fmt.Errorf(
			"dbtemplate: engine %q carries no default version (registry invariant broken)", engineName)
	}
	for _, s := range specs {
		if s.ID != version {
			continue
		}
		if s.Deprecated {
			note := ""
			if s.Note != "" {
				note = " (" + s.Note + ")"
			}
			return enginetemplate.VersionSpec{}, fmt.Errorf("%w: %s %q is deprecated%s (available: %v)",
				ErrVersionDeprecated, engineName, version, note, VersionIDs(specs))
		}
		return s, nil
	}
	return enginetemplate.VersionSpec{}, fmt.Errorf("%w: %s %q (available: %v)",
		ErrUnknownVersion, engineName, version, VersionIDs(specs))
}

// VersionIDs 报告 specs 的版本 ID 集（错误文案与目录面共用；保序）。
func VersionIDs(specs []enginetemplate.VersionSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.ID
	}
	return out
}

// InfoFor 返回 engine 的回显信息（单源：模板注册表）。B2a 过渡形：按
// Default 版本解析回显——行值直读随 B2c version 列迁移落地（消费点带
// TODO(B2c) 注释）。
func InfoFor(engine string) (Info, bool) {
	tpl, err := For(engine, "")
	if err != nil {
		return Info{}, false
	}
	return tpl.Meta(), true
}

// PasswordFromURL 从连接串提取密码（模板 URL 契约的读回面——写读同源：
// ConnURL 铸的形状在此解析；材料渲染侧的单真源回读）。数据库家族方言
// （URL 单真源），不下沉内核。
func PasswordFromURL(connectURL string) (string, error) {
	u, err := url.Parse(connectURL)
	if err != nil {
		return "", fmt.Errorf("parse database connection url: %w", err)
	}
	if u.User == nil {
		return "", fmt.Errorf("database connection url carries no credentials")
	}
	password, _ := u.User.Password()
	if password == "" {
		return "", fmt.Errorf("database connection url carries no password")
	}
	return password, nil
}
