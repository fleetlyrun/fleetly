package cmd

// R10-L1 守卫：--json golden 的机器契约强度（防空 golden 把缺口合法化）。
// 旧覆盖守卫只查「文件存在」——0 字节 golden 也算通过，于是
// `channels delete --json`（成功但 stdout 空）长期全绿（R10-M1）。
//
// golden 命名约定：`<verb>-json.golden` 指**以 --json 调用**的那一轮快照，
// 不保证文件内容是 stdout 的单 JSON 文档。按内容分四类执法：
//
//  1. stdout 单文档（绝大多数）：JSON 合法且非空（`{}` 合法——空响应对象
//     也是可解析契约；0 字节/空白不合法）；
//  2. NDJSON 帧流（events follow / logs / exec / shell）：每行一个 JSON
//     对象——逐行 json.Valid；零帧由 jsonGoldenEmptyStdout 显式登记
//     （builds logs 的空缓冲是流契约，不是缺失文档）；
//  3. 错误路径的 stderr 快照：内容是非 JSON 的错误信封（stdout 按契约空，
//     独立断言）；登记 jsonGoldenStderr 带理由，双向保鲜；
//  4. 错误/usage 路径的 stdout 空快照：登记 jsonGoldenEmptyStdout 带理由，
//     并断言其确实为空（哪天开始写 stdout，登记即过期）。
//
// 覆盖枚举的递归展开在 coverage_test.go（嵌套组员与组本身同受双形态
// golden 契约——alerts rules / apps vars 不再漏网）。

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// jsonGoldenEmptyStdout 登记「stdout 合法为空」的 --json 快照：错误/usage
// 路径（信封在 stderr）与零帧 NDJSON 流。双向保鲜：登记项必须仍为空；
// 若开始输出内容（缺口闭合或流有帧），条目即过期。
var jsonGoldenEmptyStdout = map[string]string{
	// 零帧流：NDJSON 帧流，零帧 = 零行是流契约本身。
	"builds-logs-json.golden": "NDJSON frame stream (one JSON object per line); the recent buffer is empty in the fixture, so zero frames = zero lines by contract",
	// 错误/usage 路径：错误信封在 stderr，stdout 按契约不写任何东西。
	"metrics-query-json.golden":          "service-error path (metrics.addr unconfigured): the envelope is asserted on stderr; stdout stays empty by contract",
	"metrics-query-empty-json.golden":    "usage-error path (missing PromQL argument): exit 64 + stderr envelope; stdout stays empty by contract",
	"metrics-series-offline-json.golden": "service-error path (metrics store offline): the envelope is asserted on stderr; stdout stays empty by contract",
}

// jsonGoldenStderr 登记「以 --json 调用、但快照捕获的是 stderr 错误信封」
// 的 golden（文件名 → 理由）。非 JSON 是正确形态（错误信封是人类可读
// 多行文本，不是机器对象）；登记即把这条非常规形态写成可复核的事实。
var jsonGoldenStderr = map[string]string{
	"apps-restart-never-deployed-json.golden":         "restart precheck rejection (no successful baseline to restart); errors always render on stderr, even for --json invocations",
	"apps-restart-unknown-json.golden":                "restart precheck rejection (unknown process in the frozen spec); stderr envelope",
	"components-restart-json.golden":                  "component restart rejection (provider has no managed workloads / runtime lacks the restart subface); stderr envelope",
	"databases-download-backup-json.golden":           "database action rejection (backup not found); stderr envelope",
	"databases-rotate-password-json.golden":           "database action rejection (rotation state); stderr envelope",
	"databases-rotate-password-not-ready-json.golden": "database action rejection (rotation not ready); stderr envelope",
	"databases-verify-json.golden":                    "database verify rejection (store/object unavailable); stderr envelope",
	"deploy-misuse-json.golden":                       "usage-error envelope (mutually exclusive deploy sources); stderr at exit 64",
	"registry-catalog-json.golden":                    "registry service-error envelope (managed registry unconfigured); stderr",
	"registry-tags-json.golden":                       "registry service-error envelope (managed registry unconfigured); stderr",
	"registry-tags-upstream-json.golden":              "registry upstream service-error envelope (registry unreachable); stderr",
	"templates-refresh-json.golden":                   "templates refresh rejection (catalog URL empty, embedded catalog is authoritative); stderr envelope",
	"users-accept-json.golden":                        "invitation acceptance rejection (token/identity validation); stderr envelope",
	"volumes-delete-json.golden":                      "volume delete rejection (missing/already-deleted row); stderr envelope (success form is a JSON document: volumes-delete-success-json.golden)",
	// B1 W3 确认面（confirm_golden_test.go）：错误信封在 stderr 的拒绝腿
	//（本地指引/服务端三拒绝码呈现——纯文本单行，stdout 按契约空）。
	"apps-delete-confirm-required-json.golden": "apps delete refusal without flags on a non-terminal stdin (B1 W3 local REQUIRED guidance); stderr line, stdout stays empty",
	"apps-delete-ticket-stale-json.golden":     "teardown ticket stale rejection (route-set anchor drifted); CLI presentation on stderr, stdout stays empty",
	"apps-scale-unknown-process-json.golden":   "scale rejection (unknown process in the effective spec); stderr envelope, stdout stays empty",
	"deploy-confirm-required-json.golden":      "deploy refusal without flags on a non-terminal stdin (B1 W3 local REQUIRED guidance); stderr line, stdout stays empty",
	"deploy-ticket-mismatch-json.golden":       "confirmation ticket mismatch rejection (unknown ticket); CLI presentation on stderr, stdout stays empty",
	"deploy-ticket-stale-json.golden":          "confirmation ticket stale rejection (variable environment drifted); CLI presentation on stderr, stdout stays empty",
	"rollback-confirm-required-json.golden":    "rollback refusal without flags (no rollback preview face on the API yet); stderr line, stdout stays empty",
	"rollback-ticket-mismatch-json.golden":     "rollback ticket mismatch rejection (unknown ticket); CLI presentation on stderr, stdout stays empty",
	// B1 W3 交互形态的 --json 轮：stdout = 预览 JSON 文档 + y/N 提示行 +
	//（确认轮）响应 JSON 文档——交互 chrome 与内容共面（golden 逐字钉死，
	// 非交互的纯文档形态见 *-preview-json）。
	"apps-delete-interactive-json.golden":    "interactive stdout: preview JSON document + Delete app? [y/N] prompt + response JSON document (interactive chrome shares stdout; pinned verbatim)",
	"deploy-interactive-json.golden":         "interactive stdout: preview JSON document + Deploy? [y/N] prompt + response JSON document (interactive chrome shares stdout; pinned verbatim)",
	"deploy-interactive-decline-json.golden": "interactive stdout: preview JSON document + Deploy? [y/N] prompt + aborted line (decline sends nothing)",
}

// jsonNDJSON 报告内容是否是多行 NDJSON 帧流（每行一个合法 JSON 对象）。
func jsonNDJSON(body []byte) bool {
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) < 2 {
		return false
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			return false
		}
	}
	return true
}

// TestJSONGoldensParseable：--json golden 的机器契约强度。
func TestJSONGoldensParseable(t *testing.T) {
	dir := filepath.Join("testdata", "golden")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read golden dir: %v", err)
	}
	usedEmpty := map[string]bool{}
	usedStderr := map[string]bool{}
	checked, emptyOrStderr := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "-json.golden") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // 读取本包 testdata 自有夹具
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		body := bytes.TrimSpace(data)

		if reason, ok := jsonGoldenEmptyStdout[name]; ok {
			usedEmpty[name] = true
			emptyOrStderr++
			if reason == "" {
				t.Errorf("jsonGoldenEmptyStdout entry %s must carry a reason", name)
			}
			if len(body) > 0 {
				t.Errorf("jsonGoldenEmptyStdout entry %s now emits output — remove the exemption and assert the document (the empty-stdout gap is closed)", name)
			}
			continue
		}
		if len(body) == 0 {
			t.Errorf("%s is empty — a successful --json path must emit a parseable, non-empty JSON document (an empty stdout is indistinguishable from a command that never ran; R10-M1/R10-L1)", name)
			continue
		}
		if json.Valid(body) {
			checked++
			continue
		}
		if jsonNDJSON(body) {
			checked++ // 帧流：每行已是合法 JSON 对象（人工形态另有 golden）
			continue
		}
		if reason, ok := jsonGoldenStderr[name]; ok {
			usedStderr[name] = true
			emptyOrStderr++
			if reason == "" {
				t.Errorf("jsonGoldenStderr entry %s must carry a reason", name)
			}
			continue
		}
		t.Errorf("%s is not a JSON document and is not registered as a stderr capture / NDJSON stream — machine-contract goldens must parse, or the non-document shape must be registered with a reason", name)
	}
	// 防盲：JSON golden 实测 236（2026-10-19）；低于 200 即扫描退化。
	if checked+emptyOrStderr < 200 {
		t.Fatalf("only %d --json goldens checked (expected >= 200) — guard is blind, fix the scan", checked+emptyOrStderr)
	}
	// 双向保鲜：登记项必须仍命中对应形态。
	var stale []string
	for name := range jsonGoldenEmptyStdout {
		if !usedEmpty[name] {
			stale = append(stale, "empty-stdout "+name)
		}
	}
	for name := range jsonGoldenStderr {
		if !usedStderr[name] {
			stale = append(stale, "stderr "+name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		t.Errorf("JSON golden registry entry %s no longer matches a golden file — remove the entry", name)
	}
}
