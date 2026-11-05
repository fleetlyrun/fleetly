package enginetemplate

// ValidateSettings/ValidateKnobValue 受理位单源单测（B2b，ADR-0095 决策
// 1/3 验收锚）：白名单外/范围外/enum 外/类型不符/格式错精确报错、空
// settings 恒通过、多键失配按键排序报首个（确定性——corpus 教训：报错
// 不随 map 遍历序漂移）。夹具是合成旋钮表（内核零 per-engine 知识——
// 五引擎真值域钉板住 dbtemplate_test）。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// knobFixture 是合成旋钮表（五 Kind 全覆盖 + int/size 范围 + enum 闭集）。
func knobFixture() []Knob {
	return []Knob{
		{Key: "max_connections", Kind: KnobInt, Min: "1", Max: "10000", Class: KnobStatic},
		{Key: "appendonly", Kind: KnobBool, Default: "true", Class: KnobStatic},
		{Key: "maxmemory-policy", Kind: KnobEnum, Enum: []string{"noeviction", "allkeys-lru"}, Class: KnobStatic},
		{Key: "shared_buffers", Kind: KnobSize, Min: "1mb", Max: "16gb", Class: KnobStatic},
		{Key: "log.retention.ms", Kind: KnobDuration, Class: KnobDynamic},
	}
}

func TestValidateSettingsEmptyPasses(t *testing.T) {
	assert.NoError(t, ValidateSettings(nil, nil), "nil knobs + nil settings must pass (knob-less face)")
	assert.NoError(t, ValidateSettings(knobFixture(), nil))
	assert.NoError(t, ValidateSettings(knobFixture(), map[string]string{}), "empty settings always pass")
}

func TestValidateSettingsAcceptsLegalValues(t *testing.T) {
	for _, tc := range []struct {
		key, value string
	}{
		{"max_connections", "1"},            // int 下界
		{"max_connections", "10000"},        // int 上界
		{"max_connections", "200"},          // int 中段
		{"appendonly", "true"},              // bool 真值
		{"appendonly", "false"},             // bool 假值
		{"maxmemory-policy", "noeviction"},  // enum 首值
		{"maxmemory-policy", "allkeys-lru"}, // enum 末值
		{"shared_buffers", "1mb"},           // size 下界
		{"shared_buffers", "16gb"},          // size 上界
		{"shared_buffers", "512MB"},         // size 单位不区分大小写
		{"shared_buffers", "1073741824b"},   // 裸字节形态（= 1gb，范围内）
		{"log.retention.ms", "30ms"},        // duration 各单位
		{"log.retention.ms", "7d"},
		{"log.retention.ms", "604800000ms"},
	} {
		err := ValidateSettings(knobFixture(), map[string]string{tc.key: tc.value})
		assert.NoError(t, err, "%s=%s must pass", tc.key, tc.value)
	}
}

func TestValidateSettingsRejectsUnknownKey(t *testing.T) {
	// 白名单外键拒：报错带键名 + 列合法键（排序稳定）。
	err := ValidateSettings(knobFixture(), map[string]string{"requirepass": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"requirepass"`, "error names the rejected key")
	assert.Contains(t, err.Error(), "available keys", "error lists the legal keys")
	for _, legal := range KnobKeys(knobFixture()) {
		assert.Contains(t, err.Error(), legal, "legal key %s must appear in the message", legal)
	}
	// 平台 owned 键同被白名单闭集拒（决策 2 执法面 = 白名单本身：不在
	// Knobs 里即不可设——受理位不区分「未登记」与「平台 owned」，闭集
	// 单一形态）。
	for _, owned := range []string{"requirepass", "dir", "bind", "loadmodule", "include"} {
		err := ValidateSettings(knobFixture(), map[string]string{owned: "1"})
		require.Error(t, err, "owned key %q must be rejected by the closed whitelist", owned)
		assert.Contains(t, err.Error(), "not a registered knob")
	}
}

func TestValidateSettingsRejectsOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		key, value string
	}{
		{"max_connections", "0"},     // int 下界外
		{"max_connections", "10001"}, // int 上界外
		{"max_connections", "-5"},    // 负值在 [1,10000] 外
		{"shared_buffers", "512kb"},  // size 下界外（512kb < 1mb）
		{"shared_buffers", "32gb"},   // size 上界外
	} {
		err := ValidateSettings(knobFixture(), map[string]string{tc.key: tc.value})
		require.Error(t, err, "%s=%s must be rejected", tc.key, tc.value)
		assert.Contains(t, err.Error(), tc.key, "error names the knob key")
		assert.Contains(t, err.Error(), "outside the allowed range", "error states the range violation")
	}
	// 范围报错带合法区间（int 与 size 各钉一条全文）。
	err := ValidateSettings(knobFixture(), map[string]string{"max_connections": "0"})
	assert.Contains(t, err.Error(), "[1, 10000]")
	err = ValidateSettings(knobFixture(), map[string]string{"shared_buffers": "32gb"})
	assert.Contains(t, err.Error(), "[1mb, 16gb]")
}

func TestValidateSettingsRejectsEnumOutside(t *testing.T) {
	err := ValidateSettings(knobFixture(), map[string]string{"maxmemory-policy": "lru"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"maxmemory-policy"`)
	assert.Contains(t, err.Error(), "outside the allowed enum")
	assert.Contains(t, err.Error(), "noeviction", "error lists the enum domain")
	assert.Contains(t, err.Error(), "allkeys-lru")
}

func TestValidateSettingsRejectsTypeMismatch(t *testing.T) {
	for _, tc := range []struct {
		key, value, want string
	}{
		{"appendonly", "yes", "is not a boolean"},
		{"appendonly", "1", "is not a boolean"},
		{"max_connections", "12.5", "is not an integer"},
		{"max_connections", "hundred", "is not an integer"},
		{"shared_buffers", "512", "is not a size"},
		{"shared_buffers", "mb", "is not a size"},
		{"shared_buffers", "512tb", "is not a size"},
		{"shared_buffers", "512 kb", "is not a size"},
		{"log.retention.ms", "30", "is not a duration"},
		{"log.retention.ms", "30sec", "is not a duration"},
		{"log.retention.ms", "30MS", "is not a duration"}, // duration 单位小写闭集
	} {
		err := ValidateSettings(knobFixture(), map[string]string{tc.key: tc.value})
		require.Error(t, err, "%s=%s must be rejected", tc.key, tc.value)
		assert.Contains(t, err.Error(), tc.key)
		assert.Contains(t, err.Error(), tc.want, "%s=%s", tc.key, tc.value)
	}
}

func TestValidateSettingsFirstErrorDeterministic(t *testing.T) {
	// 多键失配一次报首个，且按排序键确定：settings 含三坏键（appendonly
	// < max_connections < zz-unknown 排序序），任何 map 遍历序下都先报
	// appendonly——corpus 教训（报错漂移）的静态钉板。
	settings := map[string]string{
		"zz-unknown":      "1",
		"appendonly":      "maybe",
		"max_connections": "0",
	}
	want := ""
	for i := 0; i < 64; i++ {
		err := ValidateSettings(knobFixture(), settings)
		require.Error(t, err)
		if want == "" {
			want = err.Error()
			assert.Contains(t, want, `"appendonly"`, "first failure must be the lowest sorted key")
			assert.Contains(t, want, "is not a boolean")
			continue
		}
		assert.Equal(t, want, err.Error(), "error must not drift across map iteration orders")
	}
	// 单键内报错稳定（同一坏值反复校验）。
	for i := 0; i < 8; i++ {
		err := ValidateSettings(knobFixture(), map[string]string{"max_connections": "0"})
		assert.Equal(t, `enginetemplate: knob "max_connections": value 0 is outside the allowed range [1, 10000]`, err.Error())
	}
}

func TestValidateKnobValueRenderGate(t *testing.T) {
	// 渲染安全闸（受理位被绕过时的纵深）：敌意载荷按 Kind 收敛全拒——
	// conf/INI 注入面（换行/引号/等号/分号/空白）进不了任何 Kind 的值域。
	hostile := []string{
		"512mb\ninclude /evil",
		"true\nrequirepass x",
		"200; touch /tmp/pwned",
		"noeviction'",
		"512mb # comment",
		`allkeys-lru"`,
		"30s && pwned",
	}
	fixture := knobFixture()
	byKey := map[string]Knob{}
	for _, k := range fixture {
		byKey[k.Key] = k
	}
	for key, knob := range byKey {
		for _, payload := range hostile {
			// enum 键以 enum 值域外敌意值打（闭集天然拒）。
			assert.Error(t, ValidateKnobValue(knob, payload),
				"%s: hostile payload must fail the render gate: %q", key, payload)
		}
	}
	// 未登记 Kind：注册表失不变量的防御态。
	assert.Error(t, ValidateKnobValue(Knob{Key: "x", Kind: KnobKind("float")}, "1.5"))
	// bool/enum 闭集正面钉板（天然安全的两 Kind）。
	assert.NoError(t, ValidateKnobValue(byKey["appendonly"], "false"))
	assert.NoError(t, ValidateKnobValue(byKey["maxmemory-policy"], "allkeys-lru"))
}
