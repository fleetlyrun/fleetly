package dataservicetemplate

// 旋钮 schema 与受理位单源（B2b，ADR-0095 决策 1/3；design §3.1）：
// 白名单类型化旋钮是引擎配置的唯一通道——每家族模板包经 adapter 注册
// knob 表（Knobs()；键 = 引擎原生词，不发明中间名），受理位
// ValidateSettings 在此单源执法（白名单外键拒、类型/范围/enum 拒、精确
// 报错带键名与合法值/范围）。旋钮值渲染安全闸 ValidateKnobValue 按 Kind
// 收敛字符集（validatePassword 同族精神——conf/INI 注入防线）：受理位与
// 渲染面（Materials 折叠）共用同一单源校验，绕过受理位的敌意值在渲染面
// fail-closed。本包零 per-engine 知识（knob 表住各家族模板包——引擎名
// 反扫守卫射程内）。

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// KnobKind 是旋钮值类型（受理位与表单控件映射共用的值域）。
type KnobKind string

const (
	KnobBool     KnobKind = "bool"
	KnobInt      KnobKind = "int"
	KnobEnum     KnobKind = "enum"
	KnobDuration KnobKind = "duration"
	KnobSize     KnobKind = "size"
)

// KnobClass 是旋钮的车道分级（ADR-0095 决策 4 双车道）。
type KnobClass string

const (
	// KnobStatic：重启类——折进 Materials/Workload static 渲染面，材料/
	// spec 字节变即受监督滚动（不进运行时热应用）。
	KnobStatic KnobClass = "static"
	// KnobDynamic：运行时类——永不进滚动材料（喂入律），经
	// ApplyDynamicSettings 一次性工具容器热应用（B2d 落地）。
	KnobDynamic KnobClass = "dynamic"
)

// Knob 是一个在册旋钮（ADR-0095 决策 1；键名冻结同词条律——只增不改
// 语义）。Min/Max 以字符串承载（受理位按 Kind 解析；int 比较数值、size
// 折字节后比较）；Default 是目录回显面的引擎原生缺省（渲染面不因缺省
// 补行——空 settings 的渲染产物与无旋钮现状逐字节一致；例外形态由各
// adapter 自持，如 redis appendonly 恒渲染行）。Category 是表单分组词
// （"persistence"/"memory"/"connections"）。
type Knob struct {
	Key      string
	Kind     KnobKind
	Min      string
	Max      string
	Enum     []string
	Default  string
	Class    KnobClass
	Category string
}

// 旋钮值格式白名单（渲染安全闸的机械面：值域被收敛到无空白/无引号/无
// 换行的字符集——redis.conf 行语法空白即断、conf/INI 换行即注入，正则
// 形态即防线）。size 不区分大小写（512MB 与 512mb 同值）；duration 单位
// 小写闭集（引擎面惯例）。
var (
	knobIntRe      = regexp.MustCompile(`^-?[0-9]+$`)
	knobDurationRe = regexp.MustCompile(`^[0-9]+(ms|s|m|h|d)$`)
)

// ValidateKnobValue 校验单个旋钮值（渲染安全闸单源——受理位
// ValidateSettings 与各家族模板 Materials 折叠面共用）：按 Kind 收敛
// 字符集 + 范围/enum 检查，报错精确带键名与合法值/范围（用户可见报错
// 英文）。Kind 未登记即拒（注册表失不变量的防御态）。
func ValidateKnobValue(k Knob, value string) error {
	switch k.Kind {
	case KnobBool:
		if value != "true" && value != "false" {
			return fmt.Errorf("dataservicetemplate: knob %q: value %q is not a boolean (expected \"true\" or \"false\")", k.Key, value)
		}
	case KnobInt:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || !knobIntRe.MatchString(value) {
			return fmt.Errorf("dataservicetemplate: knob %q: value %q is not an integer", k.Key, value)
		}
		if k.Min != "" {
			lo, err := strconv.ParseInt(k.Min, 10, 64)
			if err != nil {
				return fmt.Errorf("dataservicetemplate: knob %q: registry carries a non-integer Min %q (registry invariant broken)", k.Key, k.Min)
			}
			if n < lo {
				return fmt.Errorf("dataservicetemplate: knob %q: value %d is outside the allowed range [%s, %s]", k.Key, n, k.Min, k.Max)
			}
		}
		if k.Max != "" {
			hi, err := strconv.ParseInt(k.Max, 10, 64)
			if err != nil {
				return fmt.Errorf("dataservicetemplate: knob %q: registry carries a non-integer Max %q (registry invariant broken)", k.Key, k.Max)
			}
			if n > hi {
				return fmt.Errorf("dataservicetemplate: knob %q: value %d is outside the allowed range [%s, %s]", k.Key, n, k.Min, k.Max)
			}
		}
	case KnobSize:
		bytes, err := knobSizeToBytes(value)
		if err != nil {
			return fmt.Errorf("dataservicetemplate: knob %q: value %q is not a size (expected <number><b|kb|mb|gb>, e.g. 512mb; units case-insensitive)", k.Key, value)
		}
		if k.Min != "" {
			lo, err := knobSizeToBytes(k.Min)
			if err != nil {
				return fmt.Errorf("dataservicetemplate: knob %q: registry carries a malformed Min %q (registry invariant broken)", k.Key, k.Min)
			}
			if bytes < lo {
				return fmt.Errorf("dataservicetemplate: knob %q: value %q is outside the allowed range [%s, %s]", k.Key, value, k.Min, k.Max)
			}
		}
		if k.Max != "" {
			hi, err := knobSizeToBytes(k.Max)
			if err != nil {
				return fmt.Errorf("dataservicetemplate: knob %q: registry carries a malformed Max %q (registry invariant broken)", k.Key, k.Max)
			}
			if bytes > hi {
				return fmt.Errorf("dataservicetemplate: knob %q: value %q is outside the allowed range [%s, %s]", k.Key, value, k.Min, k.Max)
			}
		}
	case KnobDuration:
		if !knobDurationRe.MatchString(value) {
			return fmt.Errorf("dataservicetemplate: knob %q: value %q is not a duration (expected <number><ms|s|m|h|d>, e.g. 30s)", k.Key, value)
		}
	case KnobEnum:
		for _, e := range k.Enum {
			if e == value {
				return nil
			}
		}
		return fmt.Errorf("dataservicetemplate: knob %q: value %q is outside the allowed enum %v", k.Key, value, k.Enum)
	default:
		return fmt.Errorf("dataservicetemplate: knob %q: unregistered kind %q (registry invariant broken)", k.Key, k.Kind)
	}
	return nil
}

// ValidateSettings 是旋钮受理位单源（ADR-0095 决策 1；受理 = 白名单完整
// 闭集执法）：settings 对 knobs 白名单全量校验——白名单外键拒（列合法
// 键）、值过 ValidateKnobValue（类型/范围/enum/格式）。空 settings 恒
// 通过（无旋钮面 = 现状）。多键失配按键排序报首个（确定性——corpus
// 教训：报错不随 map 遍历序漂移）。
func ValidateSettings(knobs []Knob, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}
	byKey := make(map[string]Knob, len(knobs))
	for _, k := range knobs {
		byKey[k.Key] = k
	}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		knob, ok := byKey[key]
		if !ok {
			return fmt.Errorf("dataservicetemplate: setting %q is not a registered knob (available keys: %v)", key, KnobKeys(knobs))
		}
		if err := ValidateKnobValue(knob, settings[key]); err != nil {
			return err
		}
	}
	return nil
}

// KnobKeys 报告旋钮表的键集（白名单外键错误的合法值面；排序稳定）。
func KnobKeys(knobs []Knob) []string {
	out := make([]string, len(knobs))
	for i, k := range knobs {
		out[i] = k.Key
	}
	sort.Strings(out)
	return out
}

// KnobSizeBytes 把受理口径的 size 值折为字节数（1024 进制；单位不区分
// 大小写）——各家族 adapter 渲染引擎原生字面的共用解析核（B2d：PG 单位名
// 大小写敏感、MySQL SET 语句不收单位后缀，size 必须经字节数重渲染；本包
// 只供解析，不承载任何引擎方言）。形态不符即错。
func KnobSizeBytes(v string) (int64, error) { return knobSizeToBytes(v) }

// knobSizeToBytes 把 size 值折为字节数（1024 进制；单位不区分大小写）。
// 形态不符即错——本函数是 ValidateKnobValue 的 size 解析核，不承担报错
// 文案。
func knobSizeToBytes(v string) (int64, error) {
	l := strings.ToLower(v)
	var num, unit string
	switch {
	case strings.HasSuffix(l, "kb"), strings.HasSuffix(l, "mb"), strings.HasSuffix(l, "gb"):
		num, unit = l[:len(l)-2], l[len(l)-2:]
	case strings.HasSuffix(l, "b"):
		num, unit = l[:len(l)-1], "b"
	default:
		return 0, fmt.Errorf("dataservicetemplate: malformed size %q", v)
	}
	if !knobIntRe.MatchString(num) {
		return 0, fmt.Errorf("dataservicetemplate: malformed size %q", v)
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("dataservicetemplate: malformed size %q", v)
	}
	switch unit {
	case "b":
		return n, nil
	case "kb":
		return n * 1024, nil
	case "mb":
		return n * 1024 * 1024, nil
	case "gb":
		return n * 1024 * 1024 * 1024, nil
	default:
		return 0, fmt.Errorf("dataservicetemplate: malformed size %q", v)
	}
}
