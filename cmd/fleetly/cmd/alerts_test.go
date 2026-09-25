package cmd

// alerts 动词组的 CLI 表驱动测试（W5-S2，D-V3W5-1）：进程内真实服务面夹具
//（internal/apitest，生产同路径 CLI-over-SDK）驱动 rules/mode/status，
// 断言往返投影、人读/JSON 双形态、mode 别名（off→unset）与服务端 409 门
// 信封。vmalert 部署行为不在 CLI 面（internal/metrics 单测覆盖）。

import (
	"strings"
	"testing"
)

func TestCLIAlertsRulesLifecycle(t *testing.T) {
	startCLI(t)

	// create：带 for/label/channels → JSON 回读同构。
	code, out, errOut := runCLIConn(t, "alerts", "rules", "create", "--json",
		"--for", "300", "--label", "severity=critical", "--channels", "ep-1,ep-2",
		"high-cpu", "cpu_used > 90")
	if code != 0 {
		t.Fatalf("create: code=%d stderr=%s", code, errOut)
	}
	for _, want := range []string{`"name": "high-cpu"`, `"for_duration_seconds": "300"`, `"severity": "critical"`, `"ep-1"`, `"ep-2"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("create output missing %q:\n%s", want, out)
		}
	}

	// ls：人读形态（缺省通道的全端点语义如实标注）。
	code, _, errOut = runCLIConn(t, "alerts", "rules", "create", "--json", "no-chan", "up == 0")
	if code != 0 {
		t.Fatalf("create second: code=%d stderr=%s", code, errOut)
	}
	code, out, _ = runCLIConn(t, "alerts", "rules", "ls")
	if code != 0 {
		t.Fatalf("ls: code=%d", code)
	}
	for _, want := range []string{"rules: 2", "high-cpu", "expr: cpu_used > 90", "for: 300s", "severity: critical", "channels: ep-1,ep-2", "no-chan", "channels: (all enabled endpoints)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ls output missing %q:\n%s", want, out)
		}
	}

	// test：VM 后端在夹具内未装配 → E_METRICS_BACKEND_UNAVAILABLE 信封
	//（CLI 诚实透传服务端错误，退出 1）。
	code, _, errOut = runCLIConn(t, "alerts", "rules", "test", "up == 0")
	if code != 1 || !strings.Contains(errOut, "E_METRICS_NOT_ENABLED") {
		t.Fatalf("test without metrics: code=%d stderr=%q, want E_METRICS_NOT_ENABLED (opt-in gate precedes backend)", code, errOut)
	}

	// rm：按名删除后 ls 归零。
	code, _, _ = runCLIConn(t, "alerts", "rules", "rm", "high-cpu")
	if code != 0 {
		t.Fatalf("rm by name: code=%d", code)
	}
	code, _, _ = runCLIConn(t, "alerts", "rules", "rm", "no-chan")
	if code != 0 {
		t.Fatalf("rm second: code=%d", code)
	}
	code, out, _ = runCLIConn(t, "alerts", "rules", "ls")
	if code != 0 || !strings.Contains(out, "rules: 0") {
		t.Fatalf("ls after rm: code=%d out=%s", code, out)
	}
	// rm 未知名 → CLI 侧解析失败（本地定位，退出 1 + 可行动文案）。
	code, _, errOut = runCLIConn(t, "alerts", "rules", "rm", "ghost")
	if code != 1 || !strings.Contains(errOut, `alert rule "ghost" not found`) {
		t.Fatalf("rm unknown: code=%d stderr=%q", code, errOut)
	}
}

func TestCLIAlertsModeAndStatus(t *testing.T) {
	startCLI(t)

	// mode on：前置门 metrics.mode=on 未满足 → 服务端 409 信封带指引。
	code, _, errOut := runCLIConn(t, "alerts", "mode", "on")
	if code != 1 || !strings.Contains(errOut, "E_ALERTS_METRICS_REQUIRED") {
		t.Fatalf("mode on without metrics: code=%d stderr=%q", code, errOut)
	}

	// 非法词 CLI 侧前置拒绝（64 用法错）。
	code, _, errOut = runCLIConn(t, "alerts", "mode", "bogus")
	if code != 64 || !strings.Contains(errOut, "on, off or unset") {
		t.Fatalf("mode bogus: code=%d stderr=%q", code, errOut)
	}

	// status 缺省态（人读）：unset / not deployed / metrics.mode unset。
	code, out, _ := runCLIConn(t, "alerts", "status")
	if code != 0 {
		t.Fatalf("status: code=%d", code)
	}
	for _, want := range []string{"mode: unset (default; not explicitly set)", "vmalert: not deployed", "rules: 0", "metrics.mode: unset"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}

	// status --json：机器形态。
	code, out, _ = runCLIConn(t, "alerts", "status", "--json")
	if code != 0 || !strings.Contains(out, `"mode": "unset"`) {
		t.Fatalf("status json: code=%d out=%s", code, out)
	}

	// 开 metrics 后 mode on 放行；off 别名回落 unset。
	code, _, errOut = runCLIConn(t, "metrics", "mode", "set", "on")
	if code != 0 {
		t.Fatalf("metrics on: code=%d stderr=%s", code, errOut)
	}
	code, out, _ = runCLIConn(t, "alerts", "mode", "on")
	if code != 0 || !strings.Contains(out, "alerts mode set to on") {
		t.Fatalf("mode on: code=%d out=%s stderr=%s", code, out, errOut)
	}
	code, out, _ = runCLIConn(t, "alerts", "mode", "off")
	if code != 0 || !strings.Contains(out, "alerts mode set to unset") {
		t.Fatalf("mode off: code=%d out=%s", code, out)
	}

	// 休眠形态：alerts on 在先、metrics 事后回 unset——status 给出诚实注记
	//（设置门只拦「alerts 先于 metrics」的开启序，事后关闭如实披露为注记）。
	code, _, errOut = runCLIConn(t, "alerts", "mode", "on")
	if code != 0 {
		t.Fatalf("re-enable alerts while metrics on: code=%d stderr=%s", code, errOut)
	}
	code, _, errOut = runCLIConn(t, "metrics", "mode", "set", "unset")
	if code != 0 {
		t.Fatalf("metrics unset: code=%d stderr=%s", code, errOut)
	}
	code, out, _ = runCLIConn(t, "alerts", "status")
	if code != 0 || !strings.Contains(out, "mode: on") || !strings.Contains(out, "vmalert: not deployed") ||
		!strings.Contains(out, "the vmalert evaluator is removed") {
		t.Fatalf("dormant status: code=%d out=\n%s", code, out)
	}
}
