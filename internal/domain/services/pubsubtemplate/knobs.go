package pubsubtemplate

// 旋钮消费面帮手（B4b，databasetemplate/knobs.go 同形——dataservicetemplate 旋钮
// 单源在本类型的分流/纵深过滤面）：SplitSettings 双车道分流（喂入律帮手）
// 与 staticKnobSettings（Materials 渲染面的纵深过滤核）。

import (
	"fmt"
	"sort"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

// SplitSettings 把 settings 按旋钮 class 分流为 staticOnly/dynamicOnly
// （ADR-0095 决策 4 喂入律帮手——pubsub 类型消费面；首批旋钮全 static，
// dynamicOnly 恒空，接口齐形保留给 kafka 族 B6）。
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

// staticKnobSettings 是 Materials 渲染的纵深过滤核（喂入律：只留本引擎
// static 旋钮，值过渲染安全闸；返回排序键序 + 过滤后 map）。
func staticKnobSettings(knobs []dataservicetemplate.Knob, settings map[string]string) ([]string, map[string]string, error) {
	staticOnly, _ := SplitSettings(knobs, settings)
	byKey := make(map[string]dataservicetemplate.Knob, len(knobs))
	for _, k := range knobs {
		byKey[k.Key] = k
	}
	keys := make([]string, 0, len(staticOnly))
	for k := range staticOnly {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if err := dataservicetemplate.ValidateKnobValue(byKey[k], staticOnly[k]); err != nil {
			return nil, nil, fmt.Errorf("pubsubtemplate: %w", err)
		}
		out[k] = staticOnly[k]
	}
	return keys, out, nil
}
