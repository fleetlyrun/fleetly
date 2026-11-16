package dataservicetemplate

// 模板注册表机制核（2026-11-16 行为不变重构，自三类型模板包逐字复制
// 下沉——MintPassword/ValidatePassword/PinnedRef 下沉先例〔enginetemplate.go〕
// 同款口径）：databasetemplate/pubsubtemplate/bucketstoretemplate 三包的
// 注册表机制函数（值域枚举 Engines、版本解析纯核 resolveVersionSpec、
// 受理弃用门 AdmitCreateVersion、数值只升恢复兼容族 numericOnlyUp/
// compareSegments/parseNumericVersion、版本 ID 集 VersionIDs、目录矩阵
// 取用 VersionsFor）原为三份同构复制（仅错误文案的包名前缀与哨兵不同），
// 真源收拢于此——各包公开函数保留签名，经一行委托/var 别名零漂移消费，
// 调用面零改。错误哨兵与文案前缀由调用方注入，各包错误文案逐字节不变
// （前缀是包名——机制错误文案的唯一包际差异轴）。
//
// 本包零 per-engine/per-type 知识的纪律不变：不持有任何注册表本体（各
// 类型包的 registry 值类型各异，经泛型 + Versioned 最小取用面承载）、
// 不含绑版出厂（versionBinder 是各包未导出协议，留在包内）。bucketstore
// 族的 AdmitCreateVersion 无「同版本带 restore 放行」分支且尾段文案不同
// （无备份面——灾备义不成立），不经本核，保留 bucketstoretemplate 本地版。

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Versioned 是注册表值的版本矩阵取用面（机制核的最小约束——各类型模板
// 包的 Template 接口天然满足；零 per-engine 知识）。
type Versioned interface {
	Versions() []VersionSpec
}

// Engines 返回注册表在册引擎值域（机制核：map 键集排序稳定；各类型包的
// registry 值类型各异，泛型承载——公开面 Engines() 一行委托）。
func Engines[V any](registry map[string]V) []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// UnknownEngineError 组装「engine 不在注册表值域」错误（机制核：哨兵与
// 合法值面由调用方注入——%w 包裹哨兵，文案与各包既有形态逐字节一致）。
func UnknownEngineError(unknownEngine error, engine string, engines []string) error {
	return fmt.Errorf("%w: %q (available engines: %v)", unknownEngine, engine, engines)
}

// ForProto 是 For 的机制核：返回注册表原型与解析出的版本条目（值域检查
// + 版本解析；绑版出厂〔versionBinder 断言〕留各包——未导出接口不可跨
// 包）。unknownEngine/unknownVersion 是调用方包的可区分哨兵，errPrefix
// 是纯机制错误文案的包名前缀。
func ForProto[V Versioned](registry map[string]V, engine, version string, unknownEngine, unknownVersion error, errPrefix string) (V, VersionSpec, error) {
	proto, ok := registry[engine]
	if !ok {
		var zero V
		return zero, VersionSpec{}, UnknownEngineError(unknownEngine, engine, Engines(registry))
	}
	spec, err := ResolveVersionSpec(engine, proto.Versions(), version, unknownVersion, errPrefix)
	if err != nil {
		var zero V
		return zero, VersionSpec{}, err
	}
	return proto, spec, nil
}

// ResolveVersion 是版本条目解析的机制核（engine 值域检查 + 空 version =
// Default；与 For 同律含弃用条目——Deprecated 门由 AdmitCreateVersion
// 承载，本面只裁「在册与否」）。
func ResolveVersion[V Versioned](registry map[string]V, engine, version string, unknownEngine, unknownVersion error, errPrefix string) (VersionSpec, error) {
	_, spec, err := ForProto(registry, engine, version, unknownEngine, unknownVersion, errPrefix)
	return spec, err
}

// ResolveVersionSpec 是版本解析纯核（databasetemplate 红灯实验直测先例的
// 真源形态）：空 version = Default；未命中列合法值；Default 缺席 = 注册表
// 失不变量。弃用条目照常解析（受理拒 vs 行解析放行分档——门在
// AdmitCreateVersion，存量/灾备/目录面不受限，ADR-0094 决策 6）。
// unknownVersion 是「version 不在该引擎在册矩阵」哨兵（%w 包裹），
// errPrefix 是纯机制错误文案的包名前缀。
func ResolveVersionSpec(engineName string, specs []VersionSpec, version string, unknownVersion error, errPrefix string) (VersionSpec, error) {
	if version == "" {
		for _, s := range specs {
			if s.Default {
				return s, nil
			}
		}
		return VersionSpec{}, fmt.Errorf(
			"%s: engine %q carries no default version (registry invariant broken)", errPrefix, engineName)
	}
	for _, s := range specs {
		if s.ID == version {
			return s, nil
		}
	}
	return VersionSpec{}, fmt.Errorf("%w: %s %q (available: %v)",
		unknownVersion, engineName, version, VersionIDs(specs))
}

// VersionIDs 报告 specs 的版本 ID 集（错误文案与目录面共用；保序）。
func VersionIDs(specs []VersionSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.ID
	}
	return out
}

// VersionsFor 返回 engine 的在册版本矩阵（目录装配与校验消费；排序稳定
// = 声明序旧→新；不存在 ok=false）。
func VersionsFor[V Versioned](registry map[string]V, engine string) ([]VersionSpec, bool) {
	tpl, ok := registry[engine]
	if !ok {
		return nil, false
	}
	return tpl.Versions(), true
}

// AdmitCreateVersion 是创建受理位的弃用门纯核（ADR-0094 决策 3/6：弃用
// 拒全新建——文案列合法值与弃用说明；**同版本带 restore 的重建放行**——
// 灾备义，弃用版卷丢失时恢复不被迫 major 升级；跨版本恢复的版本轴由
// RestoreCompat 另裁）。legal 是该引擎的合法版本 ID 集
// （VersionIDs(VersionsFor(engine))——错误文案面）；deprecated 是受理位
// 弃用门哨兵（%w 包裹）。返回 nil = 放行。bucketstore 族不经本核（无
// 备份面故无 restore 放行分支，尾段文案不同——见文件头注）。
func AdmitCreateVersion(engine string, spec VersionSpec, legal []string, hasRestore bool, deprecated error) error {
	if !spec.Deprecated || hasRestore {
		return nil
	}
	note := ""
	if spec.Note != "" {
		note = " (" + spec.Note + ")"
	}
	return fmt.Errorf("%w: %s %q is deprecated%s (available: %v); deprecated versions refuse fresh creates - restore into a current version, or recreate the same version from a backup of it (disaster recovery)",
		deprecated, engine, spec.ID, note, legal)
}

// NumericOnlyUp 是默认恢复兼容规则（机制核）：点分数值逐段比较，to <
// from 即降级拒（每段独立数值比较——mysql 8.4→9、redis 7.4→8.2 都是升；
// 9→8.4 拒）。errPrefix 是降级拒文案的包名前缀。
func NumericOnlyUp(from, to string, errPrefix string) error {
	fv, err := ParseNumericVersion(from, errPrefix)
	if err != nil {
		return err
	}
	tv, err := ParseNumericVersion(to, errPrefix)
	if err != nil {
		return err
	}
	if CompareSegments(tv, fv) < 0 {
		return fmt.Errorf("%s: restore is a version downgrade (%s -> %s); engine versions only upgrade - create the target on the backup version or newer", errPrefix, from, to)
	}
	return nil
}

// CompareSegments 逐段数值比较（点分数值轴共用：短前缀按 0 补齐——
// "9" vs "8.4" 逐段 [9]×[8,4] → 升）。返回 -1/0/1。
func CompareSegments(a, b []int) int {
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

// ParseNumericVersion 解析点分数值版本（"16"、"8.4"、"2.11"；空段/非数值
// 段 = 非 in-册形态，精确拒绝）。errPrefix 是拒绝文案的包名前缀。
func ParseNumericVersion(v string, errPrefix string) ([]int, error) {
	if v == "" {
		return nil, fmt.Errorf("%s: %q is not a numeric engine version id", errPrefix, v)
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || p == "" {
			return nil, fmt.Errorf("%s: %q is not a numeric engine version id", errPrefix, v)
		}
		out = append(out, n)
	}
	return out, nil
}
