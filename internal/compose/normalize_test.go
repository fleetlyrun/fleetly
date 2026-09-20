package compose

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// golden 测试支持 -update：go test ./internal/compose -run Golden -update
// 重新落盘期望产物（与 buf generate 后 diff 门禁同一纪律：产物提交、
// 变更须显式评审）。
var update = flag.Bool("update", false, "rewrite golden files")

// TestInterpolationDisabled 验收 3：${VAR} 插值关闭——compose 里写 ${FOO}
// → 归一化结果为字面 "${FOO}"（env hash 与值形态双重断言）。
func TestInterpolationDisabled(t *testing.T) {
	content := `
name: my-api
services:
  web:
    image: nginx
    environment:
      LITERAL: "${FOO}"
      MIXED: "prefix-${FOO}-suffix"
`
	spec := loadOK(t, writeCompose(t, content))
	svc := spec.Services[0]
	if len(svc.Environment) != 2 {
		t.Fatalf("environment entries = %d, want 2", len(svc.Environment))
	}
	if svc.Environment[0].Key != "LITERAL" {
		t.Fatalf("env[0].Key = %q", svc.Environment[0].Key)
	}
	// "${FOO}" 的 sha256——若发生插值/环境透传，hash 必然不同。
	wantHash := sha256Hash(t, "${FOO}")
	if svc.Environment[0].Hash != wantHash {
		t.Errorf("LITERAL hash = %s, want %s for literal ${FOO} (interpolation not disabled or value passed through)", svc.Environment[0].Hash, wantHash)
	}
	mixedHash := sha256Hash(t, "prefix-${FOO}-suffix")
	if svc.Environment[1].Hash != mixedHash {
		t.Errorf("MIXED hash = %s, want %s", svc.Environment[1].Hash, mixedHash)
	}
	if rawJSONContains(t, spec, "${FOO}") {
		t.Error("normalized JSON must not contain plaintext env values (even when the value is the literal ${FOO})")
	}
}

// TestDotEnvNotLoaded 验收 3（补）：同目录 .env 不参与解析（插值关闭的
// 另一面：compose-go 默认会读 .env 注入插值——本包 SkipInterpolation +
// 空 Environment 后 .env 无入口）。
func TestDotEnvNotLoaded(t *testing.T) {
	path := writeComposeWith(t, map[string]string{
		".env": "SECRET_FROM_DOTENV=leaked-value",
		"compose.yaml": `
name: my-api
services:
  web:
    image: nginx
    environment:
      GOT: "${SECRET_FROM_DOTENV}"
`,
	})
	spec := loadOK(t, path)
	env := spec.Services[0].Environment
	if len(env) != 1 || env[0].Key != "GOT" {
		t.Fatalf("environment = %+v", env)
	}
	if env[0].Hash != sha256Hash(t, "${SECRET_FROM_DOTENV}") {
		t.Errorf(".env interpolation not disabled: hash = %s", env[0].Hash)
	}
	if rawJSONContains(t, spec, "leaked-value") {
		t.Error("normalized output leaks the .env value")
	}
}

// TestEnvFileMerge 归一化 env 合并链：env_file < environment（同键
// environment 覆盖 env_file 且来源标注跟随最终层），多 env_file 按声明序
// 后者覆盖前者。
func TestEnvFileMerge(t *testing.T) {
	path := writeComposeWith(t, map[string]string{
		"compose.yaml": `
name: my-api
services:
  web:
    image: nginx
    env_file:
      - base.env
      - override.env
    environment:
      SHARED: from_environment
      ONLY_ENV: literal
`,
		"base.env": `
# comments and blank lines
BASE_ONLY=base_value
SHARED=from_base
EXPORTED=exported_value
QUOTED="quoted literal"
`,
		"override.env": `
SHARED=from_override_file
`,
	})
	spec := loadOK(t, path)
	env := spec.Services[0].Environment
	got := map[string]EnvVar{}
	for _, e := range env {
		got[e.Key] = e
	}
	if len(env) != 5 {
		t.Fatalf("environment entries = %d (%v), want 5", len(env), env)
	}
	assertEnv := func(key, wantHashOf, wantSource string) {
		t.Helper()
		e, ok := got[key]
		if !ok {
			t.Fatalf("missing env key %s: %+v", key, env)
		}
		if want := sha256Hash(t, wantHashOf); e.Hash != want {
			t.Errorf("env %s hash = %s, want %s for literal %q", key, e.Hash, want, wantHashOf)
		}
		if e.Source != wantSource {
			t.Errorf("env %s source = %s, want %s", key, e.Source, wantSource)
		}
	}
	assertEnv("BASE_ONLY", "base_value", EnvSourceEnvFile)
	assertEnv("SHARED", "from_environment", EnvSourceEnvironment)
	assertEnv("EXPORTED", "exported_value", EnvSourceEnvFile)
	assertEnv("QUOTED", "quoted literal", EnvSourceEnvFile)
	assertEnv("ONLY_ENV", "literal", EnvSourceEnvironment)
}

// TestEnvFileMissingRejected 必填 env_file 缺失显式报错（required:false
// 容忍缺省）。
func TestEnvFileMissingRejected(t *testing.T) {
	reject := writeComposeWith(t, map[string]string{
		"compose.yaml": `
name: my-api
services:
  web:
    image: nginx
    env_file: missing.env
`,
	})
	_, _, err := Load(context.Background(), reject)
	if err == nil || !strings.Contains(err.Error(), "missing.env") {
		t.Fatalf("expected an error for a missing required env_file, got %v", err)
	}

	okay := writeComposeWith(t, map[string]string{
		"compose.yaml": `
name: my-api
services:
  web:
    image: nginx
    env_file:
      - path: missing.env
        required: false
`,
	})
	loadOK(t, okay)
}

// TestEnvFileInterpolationLiteralInEnvFile env_file 内的 ${VAR} 同样按字面
// （不使用 compose-go dotenv：其解析器做文件内展开，违反字面值纪律）。
func TestEnvFileInterpolationLiteralInEnvFile(t *testing.T) {
	path := writeComposeWith(t, map[string]string{
		"compose.yaml": `
name: my-api
services:
  web:
    image: nginx
    env_file: app.env
`,
		"app.env": `
A=hello
B=${A}-world
`,
	})
	spec := loadOK(t, path)
	env := spec.Services[0].Environment
	if len(env) != 2 {
		t.Fatalf("environment = %+v, want 2 entries", env)
	}
	if env[1].Key != "B" || env[1].Hash != sha256Hash(t, "${A}-world") {
		t.Errorf("interpolation inside env_file not kept literal: %+v", env[1])
	}
}

// TestDomainNormalization 域名归一化：trim/小写/IDN→punycode/去重排序。
func TestDomainNormalization(t *testing.T) {
	content := `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.domains: "  Bücher.DE , app.example.com, bücher.de  "
`
	spec := loadOK(t, writeCompose(t, content))
	domains := spec.Services[0].Domains
	want := "app.example.com,xn--bcher-kva.de"
	if strings.Join(domains, ",") != want {
		t.Errorf("domains = %v, want %v", domains, want)
	}
}

// TestGoldenNormalization 验收 1/4：归一化 golden——§2.4 示例与扩展面
// （env_file/卷/网络/stop 字段/placement）的 canonical JSON 快照。
func TestGoldenNormalization(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		golden   string
		skipHash bool
	}{
		{name: "architecture_example", fixture: filepath.Join("testdata", "valid", "my-api.compose.yaml"), golden: "testdata/golden/my-api.golden.json"},
		{name: "extended_surface", fixture: filepath.Join("testdata", "valid", "extended.compose.yaml"), golden: "testdata/golden/extended.golden.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, _, err := Load(context.Background(), tc.fixture)
			if err != nil {
				t.Fatalf("load %s: %v", tc.fixture, err)
			}
			raw, err := spec.CanonicalJSON()
			if err != nil {
				t.Fatalf("canonical: %v", err)
			}
			goldenPath := filepath.FromSlash(tc.golden)
			if *update {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o750); err != nil {
					t.Fatalf("mkdir golden: %v", err)
				}
				if err := os.WriteFile(goldenPath, raw, 0o600); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath) //nolint:gosec // golden 为 testdata 固定路径
			if err != nil {
				t.Fatalf("read golden (generate it on a first run with -update): %v", err)
			}
			if string(raw) != string(want) {
				t.Errorf("golden mismatch:\n got: %s\nwant: %s", raw, want)
			}
			// spec_hash 稳定性：同文件两次加载 hash 一致。
			spec2, _, err := Load(context.Background(), tc.fixture)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if spec2.SpecHash != spec.SpecHash {
				t.Errorf("spec_hash unstable: %s vs %s", spec.SpecHash, spec2.SpecHash)
			}
		})
	}
}

// TestWhitelistGolden S16-C3：受控子集白名单键集（顶层 + 服务级）落 golden
// 快照——白名单增删忘改 golden/文档 → 本测试红（架构 §2.4 注记「支持集以
// internal/compose/testdata/whitelist.golden 为准」）。更新方式：
// go test ./internal/compose -run Whitelist -update。
func TestWhitelistGolden(t *testing.T) {
	var b strings.Builder
	b.WriteString("# compose 受控子集白名单真源（S16-C3；架构 §2.4「支持集以本 golden 为准」）。\n")
	b.WriteString("# 与 internal/compose/validate.go 的 topLevelWhitelist / serviceWhitelist\n")
	b.WriteString("# 集合一致性由 TestWhitelistGolden 钉死；增删键须显式更新本文件并同步\n")
	b.WriteString("# 架构文档 §2.4。子段白名单（build/healthcheck/deploy/...）不在本表——\n")
	b.WriteString("# 支持面以顶层与服务级两表为纲。\n\n")
	b.WriteString("[top_level]\n")
	for _, k := range sortedKeys(topLevelWhitelist) {
		fmt.Fprintf(&b, "%s\n", k)
	}
	b.WriteString("\n[service]\n")
	for _, k := range sortedKeys(serviceWhitelist) {
		fmt.Fprintf(&b, "%s\n", k)
	}
	raw := []byte(b.String())
	goldenPath := filepath.Join("testdata", "whitelist.golden")
	if *update {
		if err := os.WriteFile(goldenPath, raw, 0o600); err != nil {
			t.Fatalf("write whitelist golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath) //nolint:gosec // golden 为 testdata 固定路径
	if err != nil {
		t.Fatalf("read whitelist golden (generate it on a first run with -update): %v", err)
	}
	if string(raw) != string(want) {
		t.Errorf("whitelist does not match golden (adding/removing whitelist keys requires explicitly updating the golden and syncing architecture §2.4):\n got:\n%s\nwant:\n%s", raw, want)
	}
}

// TestCanonicalJSONDeterministic 字段书写顺序不影响归一化（键序无关性）。
func TestCanonicalJSONDeterministic(t *testing.T) {
	a := loadOK(t, writeCompose(t, `
name: my-api
services:
  web:
    environment:
      Z: "1"
      A: "2"
    image: nginx
`))
	b := loadOK(t, writeCompose(t, `
name: my-api
services:
  web:
    image: nginx
    environment:
      A: "2"
      Z: "1"
`))
	if a.SpecHash != b.SpecHash {
		t.Errorf("key write order affects spec_hash: %s vs %s", a.SpecHash, b.SpecHash)
	}
}

// sha256Hash 测试辅助：求字面值的 sha256 hex。
func sha256Hash(t *testing.T, v string) string {
	t.Helper()
	return sha256Hex(v)
}
