package diagnosis

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/finding"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// fixtures 是 partial-compare 三件套 runner（zeabur 形态）：
//
//	fixtures/<case>/{case.yaml, input.yaml, expected.json}
//
//   - input.yaml：Facts 值的可读 YAML（观测/事件/告警/组件四段；键 null =
//     该面读不了 None，空列表 = 可读空集 Some）；
//   - expected.json：partial-compare——写了才断言、没写不断言、null 断言
//     缺席；条目断稳定 id + 可选 workload_id/fault_class/params 局部匹配
//     （写了的键才比），不断言文案。
//
// 负向用例是主体（≥6，由 TestFixtureSuiteShape 执法）；正向每规则至少 1。

// fixtureFacts 是 input.yaml 的反序列化形态（字段即 Facts 四面 + now）。
type fixtureFacts struct {
	Now          time.Time                         `yaml:"now"`
	Observations Option[[]WorkloadObservationFact] `yaml:"observations"`
	Events       Option[[]EventFact]               `yaml:"events"`
	Alerts       Option[[]AlertFact]               `yaml:"alerts"`
	Components   Option[[]ComponentHealthFact]     `yaml:"components"`
}

// Facts 转纯值 Facts。
func (ff fixtureFacts) Facts() Facts {
	return Facts{
		Observations: ff.Observations,
		Events:       ff.Events,
		Alerts:       ff.Alerts,
		Components:   ff.Components,
		Now:          ff.Now,
	}
}

// UnmarshalYAML 是 Option 的 fixtures 反序列化面（仅测试编译期存在）：
// null 节点 = None（读不了），其余（含空列表）= Some。
func (o *Option[T]) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag == "!!null" {
		*o = None[T]()
		return nil
	}
	var v T
	if err := node.Decode(&v); err != nil {
		return err
	}
	*o = Some(v)
	return nil
}

// fixtureCase 是 case.yaml 形态（kind: positive|negative，仅形态执法用）。
type fixtureCase struct {
	Name        string `yaml:"name"`
	Kind        string `yaml:"kind"`
	Description string `yaml:"description"`
}

// expectedEntry 是单条目断言（全部可选；零值字段 = 不断言该项）。
type expectedEntry struct {
	WorkloadID string            `json:"workload_id"`
	FaultClass string            `json:"fault_class"`
	Params     map[string]string `json:"params"`
}

// expectedFile 是 expected.json 形态：Findings 值 null = 断言缺席；键缺省 =
// 不断言；Total/Unreadable 非 nil 才断言。
type expectedFile struct {
	Total      *int                      `json:"total"`
	Findings   map[string]*expectedEntry `json:"findings"`
	Unreadable *[]string                 `json:"unreadable"`
}

// TestFixtures 全量跑 fixtures：逐 case 装载 → Evaluate → partial-compare。
func TestFixtures(t *testing.T) {
	dirs := fixtureCaseDirs(t)
	require.NotEmpty(t, dirs, "fixtures suite must not be empty")
	for _, dir := range dirs {
		dir := dir
		t.Run(filepath.Base(dir), func(t *testing.T) {
			var fc fixtureCase
			loadYAML(t, filepath.Join(dir, "case.yaml"), &fc)
			require.Equal(t, filepath.Base(dir), fc.Name, "case name must match directory")
			require.Contains(t, []string{"positive", "negative"}, fc.Kind, "case kind must be positive|negative")
			require.NotEmpty(t, fc.Description, "case description required")

			var ff fixtureFacts
			loadYAML(t, filepath.Join(dir, "input.yaml"), &ff)
			facts := ff.Facts()
			got := Evaluate(facts)

			raw, err := os.ReadFile(filepath.Join(dir, "expected.json")) //nolint:gosec // 读取本包 fixtures 自有夹具
			require.NoError(t, err)
			var exp expectedFile
			require.NoError(t, json.Unmarshal(raw, &exp), "expected.json must parse")
			assertExpected(t, fc.Name, got, facts.UnreadableFaces(), exp)
		})
	}
}

// assertExpected 是 partial-compare 核心：写了才断言。
func assertExpected(t *testing.T, caseName string, got []Finding, unreadable []string, exp expectedFile) {
	t.Helper()
	if exp.Total != nil && len(got) != *exp.Total {
		t.Errorf("%s: expected %d finding(s), got %d: %s", caseName, *exp.Total, len(got), findingIDs(got))
	}
	byID := map[string][]Finding{}
	for _, f := range got {
		byID[f.ID()] = append(byID[f.ID()], f)
	}
	for id, entry := range exp.Findings {
		instances := byID[id]
		if entry == nil {
			if len(instances) > 0 {
				t.Errorf("%s: finding %s expected absent but produced %d instance(s)", caseName, id, len(instances))
			}
			continue
		}
		matched := false
		for _, f := range instances {
			if entry.WorkloadID != "" && f.WorkloadID != entry.WorkloadID {
				continue
			}
			if entry.FaultClass != "" && string(f.FaultClass) != entry.FaultClass {
				continue
			}
			if !paramsCover(f.Params, entry.Params) {
				continue
			}
			matched = true
			break
		}
		if !matched {
			t.Errorf("%s: finding %s expected with %+v but no matching instance among %d", caseName, id, entry, len(instances))
		}
	}
	if exp.Unreadable != nil {
		want := append([]string(nil), *exp.Unreadable...)
		have := append([]string(nil), unreadable...)
		sort.Strings(want)
		sort.Strings(have)
		if strings.Join(want, ",") != strings.Join(have, ",") {
			t.Errorf("%s: unreadable faces expected %v, got %v", caseName, want, have)
		}
	}
}

// paramsCover 报告 got 是否覆盖 want 的全部键值（局部匹配：没写的键不比）。
func paramsCover(got, want map[string]string) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

// findingIDs 是失败信息的紧凑形态（稳定 id，不含文案）。
func findingIDs(fs []Finding) string {
	ids := make([]string, 0, len(fs))
	for _, f := range fs {
		ids = append(ids, f.ID())
	}
	return strings.Join(ids, ",")
}

// TestFixtureSuiteShape 是 fixtures 纪律执法：三件套齐全、负向为主体（≥6）、
// 每个在册条目至少被一个正向 case 期待过（每规则至少 1 正向）。
func TestFixtureSuiteShape(t *testing.T) {
	dirs := fixtureCaseDirs(t)
	negative, positive := 0, 0
	positiveIDs := map[string]bool{}
	for _, dir := range dirs {
		var fc fixtureCase
		loadYAML(t, filepath.Join(dir, "case.yaml"), &fc)
		switch fc.Kind {
		case "negative":
			negative++
		case "positive":
			positive++
		}
		raw, err := os.ReadFile(filepath.Join(dir, "expected.json")) //nolint:gosec // 读取本包 fixtures 自有夹具
		require.NoError(t, err)
		var exp expectedFile
		require.NoError(t, json.Unmarshal(raw, &exp))
		for id, entry := range exp.Findings {
			if entry != nil {
				positiveIDs[id] = true
			}
		}
	}
	if negative < 6 {
		t.Errorf("fixtures must be negative-majority: got %d negative cases, want >= 6", negative)
	}
	if positive < 1 {
		t.Errorf("fixtures must include positive cases")
	}
	for _, id := range finding.IDs() {
		if !positiveIDs[id] {
			t.Errorf("finding %s never expected positively by any fixture (each rule needs >= 1 positive case)", id)
		}
	}
}

// fixtureCaseDirs 枚举 fixtures/ 下的 case 目录（缺三件套即红）。
func fixtureCaseDirs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("fixtures")
	require.NoError(t, err, "fixtures directory missing")
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join("fixtures", e.Name())
		for _, file := range []string{"case.yaml", "input.yaml", "expected.json"} {
			require.FileExists(t, filepath.Join(dir, file), "fixture case %s incomplete", e.Name())
		}
		dirs = append(dirs, dir)
	}
	return dirs
}

// loadYAML 读入并解析 YAML 夹具。
func loadYAML(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // 读取本包 fixtures 自有夹具
	require.NoError(t, err, "%s missing", path)
	require.NoError(t, yaml.Unmarshal(raw, out), "%s must parse", path)
}
