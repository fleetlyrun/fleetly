package databasetemplate

// 旋钮消费面帮手（B2b，ADR-0095 决策 1/4；design §3.1/§3.2）：旋钮表
// （Knobs）与平台 owned 键闭集（OwnedKeys）住各引擎 adapter（per-engine
// 知识单源）；跨类型共用面——SplitSettings 双车道分流（喂入律帮手，调用
// 方预过滤 static 子集）与 staticKnobSettings（Materials 渲染面的纵深
// 过滤核：喂入律 + 渲染安全闸）——2026-11-16 下沉 dataservicetemplate
// 单源（三类型模板包逐字相同复制收敛，MintPassword 下沉先例同款），本包
// 经 var 别名/一行委托零漂移消费，调用面零改。

import (
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

// SplitSettings 把 settings 按旋钮 class 分流为 staticOnly/dynamicOnly
// （ADR-0095 决策 4 喂入律帮手——内核单源 dataservicetemplate.SplitSettings）：
// 投影与 Materials 调用点只喂 staticOnly（dynamic 键永不进 Workload env
// 与 Materials），dynamicOnly 走 ApplyDynamicSettings 热应用面（B2d）。
// 白名单外键两侧都不落——受理位 ValidateSettings 已拒，此处静默丢弃是
// 纵深而非执法。
var SplitSettings = dataservicetemplate.SplitSettings

// staticKnobSettings 是 Materials 渲染面的共用纵深核（喂入律 + 渲染安全
// 闸；真源 dataservicetemplate.StaticKnobSettings——前缀空串 = 校验错误
// 原样透传，文案逐字节不变）：传入 settings 先按模板 knob 表过滤——只留
// static 键（dynamic/未知键结构性丢弃：调用方漏了 SplitSettings 预过滤时
// dynamic 键也到不了渲染面），每个幸存值再过 ValidateKnobValue 按 Kind
// 收敛的字符集校验（受理位被绕过时的 fail-closed——validatePassword 同
// 族精神）。返回排序稳定的键序（渲染行序确定，golden 可钉）。空 settings
// 的 nil 返回形态是本包既有契约（原实现早退），本地保留。
func staticKnobSettings(knobs []dataservicetemplate.Knob, settings map[string]string) ([]string, map[string]string, error) {
	if len(settings) == 0 {
		return nil, nil, nil
	}
	return dataservicetemplate.StaticKnobSettings(knobs, settings, "")
}
