package envlayer

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/compose"
)

// hashOf 是测试侧 sha256 hex 期望值计算。
func hashOf(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// TestMergeChainPriorityMatrix 验收 3：三层优先链矩阵——env_file <
// environment < 平台层，同键高层覆盖低层、来源标注逐条正确；输出按 key
// 字典序（确定性）。
func TestMergeChainPriorityMatrix(t *testing.T) {
	envFile := map[string]string{
		"LOG_LEVEL": "info",    // 仅 env_file
		"DB_HOST":   "file-db", // compose 覆盖
		"POOL_SIZE": "10",      // 平台覆盖
		"FEATURE_A": "on",      // 三层同键：平台胜
	}
	composeEnv := map[string]string{
		"DB_HOST":   "compose-db",
		"POOL_SIZE": "20",
		"FEATURE_A": "maybe",
		"ONLY_ENV":  "compose-value",
	}
	platform := []PlatformVar{
		{Key: "POOL_SIZE", Value: "64", Source: "platform"},
		{Key: "FEATURE_A", Value: "platform-on", Source: "platform"},
		{Key: "PLATFORM_ONLY", Value: "p", Source: "platform"},
	}

	merged, overridden := MergeChain(envFile, composeEnv, platform)

	want := map[string]struct {
		value  string
		source Source
	}{
		"DB_HOST":       {"compose-db", SourceCompose},
		"FEATURE_A":     {"platform-on", SourcePlatform},
		"LOG_LEVEL":     {"info", SourceEnvFile},
		"ONLY_ENV":      {"compose-value", SourceCompose},
		"PLATFORM_ONLY": {"p", SourcePlatform},
		"POOL_SIZE":     {"64", SourcePlatform},
	}
	if len(merged) != len(want) {
		t.Fatalf("merged rows = %d, want %d: %+v", len(merged), len(want), merged)
	}
	keys := make([]string, 0, len(merged))
	for _, m := range merged {
		keys = append(keys, m.Key)
		w := want[m.Key]
		if m.Value != w.value || m.Source != w.source {
			t.Errorf("%s = {%s %s}, want {%s %s}", m.Key, m.Value, m.Source, w.value, w.source)
		}
		if m.Hash != hashOf(m.Value) {
			t.Errorf("%s hash = %s, want sha256(%s)", m.Key, m.Hash, m.Value)
		}
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("merged not key-sorted: %v", keys)
	}

	// 覆盖键 = 平台层命中的文件层键（POOL_SIZE/FEATURE_A；PLATFORM_ONLY 无
	// 文件层同名键不算覆盖）。
	wantOverrides := []string{"FEATURE_A", "POOL_SIZE"}
	if len(overridden) != len(wantOverrides) {
		t.Fatalf("overridden = %v, want %v", overridden, wantOverrides)
	}
	for i, k := range wantOverrides {
		if overridden[i] != k {
			t.Errorf("overridden[%d] = %s, want %s", i, overridden[i], k)
		}
	}
}

// TestMergeChainSystemOverPlatform 平台层内部 system > platform（连接串
// 留位优先级，保守裁决）。
func TestMergeChainSystemOverPlatform(t *testing.T) {
	platform := []PlatformVar{
		{Key: "DATABASE_URL", Value: "user-set", Source: "platform"},
		{Key: "DATABASE_URL", Value: "system-conn", Source: "system"},
	}
	merged, _ := MergeChain(nil, nil, platform)
	if len(merged) != 1 || merged[0].Value != "system-conn" || merged[0].Source != SourceSystem {
		t.Fatalf("merged = %+v, want system-conn/system", merged)
	}
	// system 同样覆盖文件层键 → 计入覆盖清单。
	_, overridden := MergeChain(map[string]string{"DATABASE_URL": "file"}, nil, platform)
	if len(overridden) != 1 || overridden[0] != "DATABASE_URL" {
		t.Fatalf("overridden = %v", overridden)
	}
}

// TestEngineMergeFaceIncludesPending S16-C4 契约统一：合并输入面——
// pending/effective 是状态层概念，本层纯函数不感知；唯一合并消费方（发布
// 引擎）传**全量**平台层行（state.ListAppEnv）：pending 参与合并、部署即
// 消费点（成功后 MarkAppEnvEffective 提升——见 engine.go
// platformEnvForMerge / observing.go succeedDeployment）。本用例钉死该
// 调用面：平台层输入含尚未提升（pending）的键时同样覆盖文件层——「随
// 本次部署生效」由引擎输入面结构性成立，而非合并函数过滤。
func TestEngineMergeFaceIncludesPending(t *testing.T) {
	envFile := map[string]string{"TOKEN": "file-token"}
	// 平台层该键尚为 pending（未经部署提升）：引擎面合并仍以平台层为准。
	// （对照：平台层不含该键 → 文件层值保留，见 TestMergeChainPriorityMatrix
	// 的 LOG_LEVEL。）
	platform := []PlatformVar{{Key: "TOKEN", Value: "platform-token", Source: "platform"}}
	merged, overridden := MergeChain(envFile, nil, platform)
	if len(merged) != 1 || merged[0].Value != "platform-token" || merged[0].Source != SourcePlatform {
		t.Fatalf("engine face merged = %+v, want platform-token/platform", merged)
	}
	if len(overridden) != 1 || overridden[0] != "TOKEN" {
		t.Fatalf("overridden = %v, want [TOKEN]", overridden)
	}
}

// TestPlatformOverrideWarnings 验收 3 的 plan 侧：W_ENV_PLATFORM_OVERRIDE
// 由平台覆盖键并入计划警告（含 pending——下次部署即生效点）。
func TestPlatformOverrideWarnings(t *testing.T) {
	spec := &compose.Spec{
		Name: "my-api",
		Services: []compose.Service{
			{
				Name: "web",
				Environment: []compose.EnvVar{
					{Key: "POOL_SIZE", Hash: hashOf("20"), Source: "environment"},
					{Key: "LOCAL_ONLY", Hash: hashOf("x"), Source: "env_file"},
				},
			},
			{Name: "worker"}, // 无 env
		},
	}
	platform := []PlatformVar{
		{Key: "POOL_SIZE", Value: "64", Source: "platform"},
		{Key: "UNRELATED", Value: "1", Source: "platform"},
	}
	warnings := PlatformOverrideWarnings(spec, platform)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v, want 1", warnings)
	}
	w := warnings[0]
	if w.Code != "W_ENV_PLATFORM_OVERRIDE" || w.Service != "web" {
		t.Fatalf("warning = %+v", w)
	}
	if !strings.Contains(w.Message, "POOL_SIZE") {
		t.Fatalf("warning message missing key: %s", w.Message)
	}
	// 空平台层 / nil spec 无警告。
	if got := PlatformOverrideWarnings(spec, nil); got != nil {
		t.Fatalf("nil platform warnings = %+v", got)
	}
}
