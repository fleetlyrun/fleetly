package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lynx-go/commands"
)

// runCLI 以给定参数驱动完整 CLI（同路径复用 newApp；不起子进程、不改
// os.Args/CWD——lynx-go/commands 不读进程参数，无 Runner 隔离问题）。
// 返回退出码与 stdout/stderr 内容。
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	env := &commands.Environment{Stdout: &stdout, Stderr: &stderr}
	code := newApp().Run(context.Background(), env, args)
	return code, stdout.String(), stderr.String()
}

// writeFixture 落盘 compose 夹具并返回绝对路径。
func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

const cliValid = `
name: my-api
services:
  web:
    image: nginx:1.27
    expose: ["8080"]
    healthcheck:
      test: ["CMD", "hc"]
    labels:
      fleetly.domains: "api.example.com"
`

const cliValidWithEnvEntry = `
name: my-api
services:
  web:
    image: nginx:1.27
    environment:
      DB_CREDENTIAL: super-secret-plaintext
    healthcheck:
      test: ["CMD", "hc"]
`

const cliInvalid = `
name: my-api
services:
  web:
    image: nginx:1.27
    deploy:
      update_config:
        failure_action: rollback
`

const cliOther = `
name: my-api
services:
  web:
    image: nginx:1.27
    expose: ["8080"]
    healthcheck:
      test: ["CMD", "hc"]
    labels:
      fleetly.domains: "api.example.com"
    environment:
      LOG_LEVEL: debug
  worker:
    image: my/worker
    command: run
`

// TestCLIVersion 冒烟：动词注册与帮助面完好。
func TestCLIVersion(t *testing.T) {
	code, out, _ := runCLI(t, "version")
	if code != 0 || !strings.Contains(out, "fleetly") {
		t.Fatalf("version: code=%d out=%q", code, out)
	}
	code, out, _ = runCLI(t)
	if code != 0 || !strings.Contains(out, "validate") || !strings.Contains(out, "plan") || !strings.Contains(out, "diff") {
		t.Fatalf("help: code=%d out=%q", code, out)
	}
}

// TestCLIValidateExitCodes 验收 5：validate 二态（0 有效 / 1 校验失败，
// 错误信封四件套上 stderr）。
func TestCLIValidateExitCodes(t *testing.T) {
	code, out, _ := runCLI(t, "validate", writeFixture(t, cliValid))
	if code != 0 || !strings.Contains(out, "my-api: valid") {
		t.Fatalf("valid compose: code=%d out=%q", code, out)
	}

	code, _, errOut := runCLI(t, "validate", writeFixture(t, cliInvalid))
	if code != 1 {
		t.Fatalf("invalid compose: code=%d, 期望 1", code)
	}
	for _, want := range []string{"E_COMPOSE_MANAGED_FIELD", "suggestion:", "docs:"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr 缺 %q:\n%s", want, errOut)
		}
	}

	// 不存在的文件 → 1。
	code, _, _ = runCLI(t, "validate", filepath.Join(t.TempDir(), "nope.yaml"))
	if code != 1 {
		t.Fatalf("missing file: code=%d, 期望 1", code)
	}
}

// TestCLIValidateJSON --json 形态可解析且携带 spec_hash。
func TestCLIValidateJSON(t *testing.T) {
	code, out, _ := runCLI(t, "validate", "--json", writeFixture(t, cliValid))
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var result struct {
		Valid    bool   `json:"valid"`
		Name     string `json:"name"`
		SpecHash string `json:"spec_hash"`
		Services int    `json:"services"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("stdout 不是 JSON: %v\n%s", err, out)
	}
	if !result.Valid || result.Name != "my-api" || result.SpecHash == "" || result.Services != 1 {
		t.Errorf("validate JSON = %+v", result)
	}
}

// TestCLIPlanThreeState 验收 4/5：plan 三态退出码——0 无变化（自基线）、
// 2 有变化（异基线）、1 错误（--baseline 本地形态；RPC 基线形态在
// golden_test.go 的服务面夹具上覆盖）。
func TestCLIPlanThreeState(t *testing.T) {
	good := writeFixture(t, cliValid)

	// 自基线（同文件）→ 无变化 → 0。
	code, out, _ := runCLI(t, "plan", "--baseline", good, good)
	if code != 0 {
		t.Fatalf("no changes: code=%d, 期望 0\n%s", code, out)
	}
	if !strings.Contains(out, "no changes") {
		t.Errorf("无变化报告缺 no changes:\n%s", out)
	}

	// 异基线 → 有变化 → 2。
	code, _, _ = runCLI(t, "plan", "--baseline", writeFixture(t, cliOther), good)
	if code != 2 {
		t.Fatalf("changed: code=%d, 期望 2", code)
	}

	// 校验失败 → 1（stderr 出错误码）。
	code, _, errOut := runCLI(t, "plan", writeFixture(t, cliInvalid))
	if code != 1 || !strings.Contains(errOut, "E_COMPOSE_MANAGED_FIELD") {
		t.Fatalf("error: code=%d stderr=%q", code, errOut)
	}
}

// TestCLIPlanJSONArtifact 验收 4：--json artifact 含 etag（spec_hash）；
// 脱敏负面测试——env 明文值不出现在任何输出流。
func TestCLIPlanJSONArtifact(t *testing.T) {
	target := writeFixture(t, cliValidWithEnvEntry)
	// 基线 = 同服务但 env 旧值（首部署只有 added，字段级差分需基线）。
	baseline := writeFixture(t, strings.Replace(cliValidWithEnvEntry, "super-secret-plaintext", "old-plaintext", 1))
	code, out, errOut := runCLI(t, "plan", "--baseline", baseline, "--json", target)
	if code != 2 {
		t.Fatalf("code=%d\nstdout=%s\nstderr=%s", code, out, errOut)
	}
	var plan struct {
		SpecHash                   string `json:"spec_hash"`
		HasChanges                 bool   `json:"has_changes"`
		Destructive                bool   `json:"destructive"`
		RequiresConfirmDestructive bool   `json:"requires_confirm_destructive"`
		Services                   struct {
			Added   []string `json:"added"`
			Removed []string `json:"removed"`
			Updated []struct {
				Name   string `json:"name"`
				Fields []struct {
					Path string `json:"path"`
					To   any    `json:"to"`
				} `json:"fields"`
			} `json:"updated"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("artifact 不是 JSON: %v\n%s", err, out)
	}
	if plan.SpecHash == "" || !plan.HasChanges {
		t.Errorf("artifact = %+v", plan)
	}
	if len(plan.Services.Updated) != 1 || plan.Services.Updated[0].Name != "web" {
		t.Fatalf("Updated = %+v", plan.Services.Updated)
	}
	// env 字段差分只含 hash 形态。
	foundEnvHash := false
	for _, f := range plan.Services.Updated[0].Fields {
		if strings.HasPrefix(f.Path, "environment.DB_CREDENTIAL") {
			foundEnvHash = true
		}
	}
	if !foundEnvHash {
		t.Errorf("缺 environment.DB_CREDENTIAL 差分: %+v", plan.Services.Updated[0].Fields)
	}
	// 负面测试：明文值（新值与基线旧值）不得出现在 stdout/stderr 任一流。
	if strings.Contains(out, "super-secret-plaintext") || strings.Contains(errOut, "super-secret-plaintext") ||
		strings.Contains(out, "old-plaintext") || strings.Contains(errOut, "old-plaintext") {
		t.Error("plan 输出泄露 env 明文值")
	}
}

// TestCLIPlanDestructive 验收 4：破坏性标记（服务删除）出现在 artifact，
// 人读报告提示 --confirm-destructive。
func TestCLIPlanDestructive(t *testing.T) {
	base := writeFixture(t, cliOther)
	target := writeFixture(t, cliValid) // 少了 worker 服务

	code, out, _ := runCLI(t, "plan", "--baseline", base, "--json", target)
	if code != 2 {
		t.Fatalf("code=%d\n%s", code, out)
	}
	var plan struct {
		Destructive                bool `json:"destructive"`
		RequiresConfirmDestructive bool `json:"requires_confirm_destructive"`
		Services                   struct {
			Removed []string `json:"removed"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if len(plan.Services.Removed) != 1 || plan.Services.Removed[0] != "worker" {
		t.Fatalf("Removed = %v, 期望 [worker]", plan.Services.Removed)
	}
	if !plan.Destructive || !plan.RequiresConfirmDestructive {
		t.Errorf("破坏性标记缺失: %+v", plan)
	}

	// 人读报告带 --confirm-destructive 提示。
	code, out, _ = runCLI(t, "plan", "--baseline", base, target)
	if code != 2 || !strings.Contains(out, "--confirm-destructive") {
		t.Fatalf("人读破坏性提示缺失: code=%d\n%s", code, out)
	}
}

// TestCLIPlanOutputArtifact --output 落盘 artifact（etag 机制位；--baseline
// 本地形态，不依赖 daemon）。
func TestCLIPlanOutputArtifact(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "plan.json")
	base := writeFixture(t, cliValid)
	target := writeFixture(t, strings.Replace(cliValid, "nginx:1.27", "nginx:1.29", 1))
	code, out, _ := runCLI(t, "plan", "--baseline", base, "--output", artifactPath, target)
	if code != 2 || !strings.Contains(out, "plan artifact written to") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	raw, err := os.ReadFile(artifactPath) //nolint:gosec // artifact 为本测试 --output 落盘的临时路径
	if err != nil {
		t.Fatalf("artifact 未落盘: %v", err)
	}
	var plan struct {
		SpecHash string `json:"spec_hash"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil || plan.SpecHash == "" {
		t.Fatalf("落盘 artifact 非法: %v %s", err, raw)
	}
}

// TestCLIDiffThreeState diff 与 plan 同三态。
func TestCLIDiffThreeState(t *testing.T) {
	a := writeFixture(t, cliValid)
	code, out, _ := runCLI(t, "diff", a, a)
	if code != 0 || !strings.Contains(out, "no changes") {
		t.Fatalf("same: code=%d out=%q", code, out)
	}
	code, _, _ = runCLI(t, "diff", a, writeFixture(t, cliOther))
	if code != 2 {
		t.Fatalf("different: code=%d, 期望 2", code)
	}
	code, _, errOut := runCLI(t, "diff", a, writeFixture(t, cliInvalid))
	if code != 1 || !strings.Contains(errOut, "E_COMPOSE_MANAGED_FIELD") {
		t.Fatalf("error: code=%d stderr=%q", code, errOut)
	}
	// 参数数量违规 → 用法错误（64，S17-D3）。
	code, _, _ = runCLI(t, "diff", a)
	if code != 64 {
		t.Fatalf("usage: code=%d, 期望 64", code)
	}
}

// TestCLIUsageExitCodes S17-D3：用法类错误（未知动词/flag 解析失败/位置
// 参数违规）退出 64（EX_USAGE 惯例）——与 plan/diff「检测到变化」的 2
// 分离，Agent/脚本可区分"有漂移"与"调用姿势错误"。
func TestCLIUsageExitCodes(t *testing.T) {
	// 未知动词 → 64。
	code, _, _ := runCLI(t, "no-such-verb")
	if code != 64 {
		t.Fatalf("unknown verb: code=%d, 期望 64", code)
	}
	// flag 解析失败 → 64。
	code, _, _ = runCLI(t, "validate", "--nope", writeFixture(t, cliValid))
	if code != 64 {
		t.Fatalf("bad flag: code=%d, 期望 64", code)
	}
	// 位置参数缺失 → 64。
	code, _, _ = runCLI(t, "validate")
	if code != 64 {
		t.Fatalf("missing arg: code=%d, 期望 64", code)
	}
	// 对照：plan 有变化仍是 2（不与用法错误混用）。
	code, _, _ = runCLI(t, "plan", "--baseline", writeFixture(t, cliOther), writeFixture(t, cliValid))
	if code != 2 {
		t.Fatalf("changes: code=%d, 期望 2", code)
	}
}
