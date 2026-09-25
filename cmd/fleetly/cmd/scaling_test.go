package cmd

// apps scaling 动词组的 CLI 表驱动测试（W5-S1）：进程内真实服务面夹具
//（internal/apitest，生产同路径 CLI-over-SDK）驱动 set/show/rm 三动词，
// 断言往返投影、人读/JSON 双形态、参数校验（词表在 CLI 侧前置拒绝）与
// 404 信封。引擎求值行为不在 CLI 面（internal/engine 单测覆盖）。

import (
	"strings"
	"testing"
)

func TestCLIScalingSmoke(t *testing.T) {
	env := startCLI(t)
	env.CreateApp(t, "scalidemo")

	// set：默认参数（min1/max4/cpu60/mem70/cd180）→ JSON 回读同构。
	code, out, errOut := runCLIConn(t, "apps", "scaling", "set", "--json", "scalidemo", "web")
	if code != 0 {
		t.Fatalf("set: code=%d stderr=%s", code, errOut)
	}
	for _, want := range []string{`"min_replicas": 1`, `"max_replicas": 4`, `"target_cpu_pct": 60`, `"cooldown_seconds": 180`} {
		if !strings.Contains(out, want) {
			t.Fatalf("set output missing %q:\n%s", want, out)
		}
	}

	// set：显式旗标整行替换。
	code, out, _ = runCLIConn(t, "apps", "scaling", "set", "--json",
		"--min", "2", "--max", "8", "--cpu", "50", "--mem", "0", "--cooldown", "300",
		"scalidemo", "web")
	if code != 0 {
		t.Fatalf("re-set: code=%d", code)
	}
	for _, want := range []string{`"min_replicas": 2`, `"max_replicas": 8`, `"target_cpu_pct": 50`} {
		if !strings.Contains(out, want) {
			t.Fatalf("re-set output missing %q:\n%s", want, out)
		}
	}
	// 零值维度字段不渲染（EmitUnpopulated=false 语义）——mem=0 即字段缺席，
	// 与「0 = 该维度不设目标」的读面契约一致。
	if strings.Contains(out, `"target_mem_pct"`) {
		t.Fatalf("re-set output should omit the zero mem dimension:\n%s", out)
	}

	// show：人读形态。
	code, out, _ = runCLIConn(t, "apps", "scaling", "show", "scalidemo", "web")
	if code != 0 {
		t.Fatalf("show: code=%d", code)
	}
	for _, want := range []string{"app: scalidemo", "service: web", "min/max replicas: 2/8", "targets: cpu 50%, mem 0%", "cooldown: 300s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show output missing %q:\n%s", want, out)
		}
	}

	// rm：删除后 show 落 404 信封（未配置即无策略）。
	code, out, _ = runCLIConn(t, "apps", "scaling", "rm", "--json", "scalidemo", "web")
	if code != 0 || !strings.Contains(out, `"removed": true`) {
		t.Fatalf("rm: code=%d out=%s", code, out)
	}
	code, _, errOut = runCLIConn(t, "apps", "scaling", "show", "scalidemo", "web")
	if code != 1 || !strings.Contains(errOut, "no scaling policy") {
		t.Fatalf("show after rm: code=%d stderr=%q", code, errOut)
	}
}

func TestCLIScalingValidationAndErrors(t *testing.T) {
	env := startCLI(t)
	env.CreateApp(t, "scalibad")

	// CLI 侧前置校验（不发请求即拒绝，退出 1 + 可行动文案）。
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"min below floor", []string{"--min", "0"}, "--min must be >= 1"},
		{"max above ceiling", []string{"--max", "17"}, "--max must be <= 16"},
		{"max below min", []string{"--min", "4", "--max", "3"}, "--max must be >= --min"},
		{"cpu target out of range", []string{"--cpu", "19"}, "within [20,90]"},
		{"no target at all", []string{"--cpu", "0", "--mem", "0"}, "at least one target is required"},
		{"cooldown out of range", []string{"--cooldown", "59"}, "within [60,3600]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"apps", "scaling", "set"}, append(tc.args, "scalibad", "web")...)
			code, _, errOut := runCLIConn(t, args...)
			if code != 1 || !strings.Contains(errOut, tc.want) {
				t.Fatalf("code=%d stderr=%q, want exit 1 containing %q", code, errOut, tc.want)
			}
		})
	}

	// 服务端 404：未设置策略的 app/service。
	code, _, errOut := runCLIConn(t, "apps", "scaling", "rm", "scalibad", "web")
	if code != 1 || !strings.Contains(errOut, "no scaling policy") {
		t.Fatalf("rm unset: code=%d stderr=%q", code, errOut)
	}
}
