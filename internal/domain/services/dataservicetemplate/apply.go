package dataservicetemplate

// dynamic 旋钮热应用的契约面（B2d，ADR-0095 决策 4 dynamic 车道；design
// §3.2）：ApplySpec 是一次热应用的引擎渲染产物，ApplyDynamicSettings 是
// 各家族模板 adapter 的实现面（本包只承载形状，零 per-engine 知识——
// 引擎知识单源纪律）。执行载体 = RuntimeUtility 一次性工具容器
// （RotatePassword 先例：argv 纯数组零 shell、凭证走 SecretFiles 面）。
// 持久化优先引擎自持久（PG ALTER SYSTEM → postgresql.auto.conf 住数据卷
// / MySQL SET PERSIST → mysqld-auto.cnf——重启自愈）；redis 无自持久面，
// 首批旋钮全 static（ADR-0095 决策 4 取舍）。

import (
	"errors"
	"fmt"
	"sort"
)

// ApplySpec 是一次 dynamic 旋钮热应用的引擎渲染产物（RotateSpec 同族）：
// argv 纯数组、零 shell 拼串（shellguard 射程不变）；凭证材料经
// SecretFiles（pgpass / defaults-extra-file 方言复用备份面）；多旋钮一次
// 渲染 = 一条命令多语句（引擎语句原子性承载——部分失败由调用方的
// 失败强制 Forget 拉回语义收口，ADR-0095 决策 6）。
type ApplySpec struct {
	Argv        []string
	Env         map[string]string
	SecretFiles map[string][]byte
}

// ErrNoDynamicFace 是「该引擎无 dynamic 面」的可区分哨兵（受理位文案面：
// 引擎在册旋钮全 static 时，dynamic 热应用不是该引擎的能力——正常分流
// 下不可达〔SplitSettings 产空 dynamic 子集即跳过热应用〕，防御态到达
// 即拒绝而非静默成功）。
var ErrNoDynamicFace = errors.New("dataservicetemplate: engine carries no dynamic knob face")

// ValidateDynamicSettings 渲染面前置校验（受理位 ValidateSettings 之外的
// 纵深核）：传入 settings 必须全数落在 knobs 的 dynamic 子集内且值过
// ValidateKnobValue（喂入律的 dynamic 半边——static 键/白名单外键到达
// 热应用面即 fail-closed）。返回按键排序的键序（确定性——argv 语句序
// 稳定，golden 可钉）。
func ValidateDynamicSettings(knobs []Knob, settings map[string]string) ([]string, map[string]string, error) {
	if len(settings) == 0 {
		return nil, nil, nil
	}
	byKey := make(map[string]Knob, len(knobs))
	for _, k := range knobs {
		byKey[k.Key] = k
	}
	dyn := map[string]string{}
	for key, value := range settings {
		knob, ok := byKey[key]
		if !ok {
			return nil, nil, fmt.Errorf("dataservicetemplate: setting %q is not a registered knob", key)
		}
		if knob.Class != KnobDynamic {
			return nil, nil, fmt.Errorf("dataservicetemplate: setting %q is class %q - only dynamic knobs reach the hot-apply face", key, knob.Class)
		}
		if err := ValidateKnobValue(knob, value); err != nil {
			return nil, nil, err
		}
		dyn[key] = value
	}
	keys := make([]string, 0, len(dyn))
	for k := range dyn {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, dyn, nil
}
