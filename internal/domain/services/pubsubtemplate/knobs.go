package pubsubtemplate

// 旋钮消费面帮手（B4b，databasetemplate/knobs.go 同形——dataservicetemplate 旋钮
// 单源在本类型的分流/纵深过滤面）：SplitSettings 双车道分流（喂入律帮手）
// 与 staticKnobSettings（Materials 渲染面的纵深过滤核）。2026-11-16 两核
// 下沉 dataservicetemplate 单源（三类型模板包逐字相同复制收敛，MintPassword
// 下沉先例同款），本包经 var 别名/一行委托零漂移消费，调用面零改。

import (
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

// SplitSettings 把 settings 按旋钮 class 分流为 staticOnly/dynamicOnly
// （ADR-0095 决策 4 喂入律帮手——内核单源 dataservicetemplate.SplitSettings；
// pubsub 类型消费面；首批旋钮全 static，dynamicOnly 恒空，接口齐形保留给
// kafka 族 B6）。
var SplitSettings = dataservicetemplate.SplitSettings

// staticKnobSettings 是 Materials 渲染的纵深过滤核（喂入律：只留本引擎
// static 旋钮，值过渲染安全闸；返回排序键序 + 过滤后 map。真源
// dataservicetemplate.StaticKnobSettings——前缀注入本包词面，校验错误
// 包装文案逐字节不变）。
func staticKnobSettings(knobs []dataservicetemplate.Knob, settings map[string]string) ([]string, map[string]string, error) {
	return dataservicetemplate.StaticKnobSettings(knobs, settings, "pubsubtemplate")
}
