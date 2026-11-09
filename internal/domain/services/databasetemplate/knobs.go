package databasetemplate

// 旋钮消费面帮手（B2b，ADR-0095 决策 1/4；design §3.1/§3.2）：旋钮表
// （Knobs）与平台 owned 键闭集（OwnedKeys）住各引擎 adapter（per-engine
// 知识单源）；本文件承载跨引擎共用面——SplitSettings 双车道分流（喂入律
// 帮手，调用方预过滤 static 子集）与 staticKnobSettings（Materials 渲染
// 面的纵深过滤核：喂入律 + 渲染安全闸）。

import (
	"sort"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

// SplitSettings 把 settings 按旋钮 class 分流为 staticOnly/dynamicOnly
// （ADR-0095 决策 4 喂入律帮手）：投影与 Materials 调用点只喂 staticOnly
// （dynamic 键永不进 Workload env 与 Materials），dynamicOnly 走
// ApplyDynamicSettings 热应用面（B2d）。白名单外键两侧都不落——受理位
// ValidateSettings 已拒，此处静默丢弃是纵深而非执法。
func SplitSettings(knobs []dataservicetemplate.Knob, settings map[string]string) (staticOnly, dynamicOnly map[string]string) {
	staticOnly, dynamicOnly = map[string]string{}, map[string]string{}
	byKey := make(map[string]dataservicetemplate.Knob, len(knobs))
	for _, k := range knobs {
		byKey[k.Key] = k
	}
	for key, value := range settings {
		knob, ok := byKey[key]
		if !ok {
			continue
		}
		switch knob.Class {
		case dataservicetemplate.KnobStatic:
			staticOnly[key] = value
		case dataservicetemplate.KnobDynamic:
			dynamicOnly[key] = value
		}
	}
	return staticOnly, dynamicOnly
}

// staticKnobSettings 是 Materials 渲染面的共用纵深核（喂入律 + 渲染安全
// 闸）：传入 settings 先按模板 knob 表过滤——只留 static 键（dynamic/
// 未知键结构性丢弃：调用方漏了 SplitSettings 预过滤时 dynamic 键也到
// 不了渲染面），每个幸存值再过 dataservicetemplate.ValidateKnobValue 按 Kind
// 收敛的字符集校验（受理位被绕过时的 fail-closed——validatePassword 同
// 族精神）。返回排序稳定的键序（渲染行序确定，golden 可钉）。
func staticKnobSettings(knobs []dataservicetemplate.Knob, settings map[string]string) ([]string, map[string]string, error) {
	if len(settings) == 0 {
		return nil, nil, nil
	}
	byKey := make(map[string]dataservicetemplate.Knob, len(knobs))
	for _, k := range knobs {
		byKey[k.Key] = k
	}
	static := map[string]string{}
	for key, value := range settings {
		knob, ok := byKey[key]
		if !ok || knob.Class != dataservicetemplate.KnobStatic {
			continue
		}
		if err := dataservicetemplate.ValidateKnobValue(knob, value); err != nil {
			return nil, nil, err
		}
		static[key] = value
	}
	keys := make([]string, 0, len(static))
	for k := range static {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, static, nil
}
