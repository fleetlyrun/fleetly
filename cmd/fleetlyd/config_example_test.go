package main

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// F4（S20 交付/CI 修正）：config-example.yaml 键集防漂移测试。
//
//   - 方向一（必红）：例中每个键必须存在于 AppConfig 的 mapstructure 键集
//     ——拦截两类缺陷：例文拼写错误键（照抄即静默失效）、AppConfig 改名/
//     删键后例文未跟（例文漂移）。
//   - 方向二（仅快照不红）：AppConfig 每个键允许在例中缺失（文件缺省即文档
//     默认），差异打印进测试日志便于人审——新增配置键时顺手补例文即可，
//     不强制（缺省回落语义本身是契约）。
//
// 框架级键（lynx 直接消费、不经 AppConfig）登记进 frameworkKeys 白名单：
// 当前仅 logging.level（lynx LogLevelFromConfig 的约定键链
// logging.level → log-level → log_level 的首选形态）。
var frameworkKeys = map[string]bool{
	"logging.level": true,
}

// flattenYAMLKeys 把 YAML 文档展平成点分键路径集（标量/数组为叶子）。
func flattenYAMLKeys(t *testing.T, node any, prefix string, out map[string]bool) {
	t.Helper()
	m, ok := node.(map[string]any)
	if !ok {
		out[prefix] = true
		return
	}
	for k, child := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		flattenYAMLKeys(t, child, path, out)
	}
}

// appConfigKeys 反射 AppConfig 的 mapstructure 标签，递归产出点分键集
// （指针字段按其元素类型处理——*bool 等标量开关；嵌套结构体继续下钻）。
func appConfigKeys(t *testing.T) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	var walk func(rt reflect.Type, prefix string)
	walk = func(rt reflect.Type, prefix string) {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			tag := f.Tag.Get("mapstructure")
			if tag == "" || tag == "-" {
				continue
			}
			name := strings.Split(tag, ",")[0]
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				walk(ft, path)
				continue
			}
			keys[path] = true
		}
	}
	walk(reflect.TypeOf(AppConfig{}), "")
	return keys
}

// TestConfigExampleKeysSubsetOfAppConfig 是例文防漂移门禁（机制验收本体）：
// 例文增删键、或 AppConfig 键改名而不例文不跟，本测试即红。
func TestConfigExampleKeysSubsetOfAppConfig(t *testing.T) {
	raw, err := os.ReadFile("../../config-example.yaml")
	if err != nil {
		t.Fatalf("read config-example.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse config-example.yaml: %v", err)
	}
	example := map[string]bool{}
	flattenYAMLKeys(t, doc, "", example)
	if len(example) == 0 {
		t.Fatal("config-example.yaml parsed to an empty key set — example file is broken")
	}

	supported := appConfigKeys(t)
	if len(supported) == 0 {
		t.Fatal("AppConfig key set is empty — reflection walk is broken")
	}

	// 方向一：例中键必须被支持（AppConfig 键或框架白名单键）。
	var unknown []string
	for k := range example {
		if !supported[k] && !frameworkKeys[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Errorf("config-example.yaml 含 AppConfig 不支持的键（拼写错误，或键已改名/移除而例文未跟；键集单一事实源 = cmd/fleetlyd/config.go）：\n  %s",
			strings.Join(unknown, "\n  "))
	}

	// 方向二：仅差异快照（允许缺失，不红）——新增配置键时以此清单为
	// 补例文的提醒。
	var missing []string
	for k := range supported {
		if !example[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	t.Logf("AppConfig 支持、但 config-example.yaml 未列出的键（允许缺失——缺省即文档默认；快照供审）：%v", missing)
}
