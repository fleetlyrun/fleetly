package cmd

// 诊断动词 golden 双形态（B2a W3，zeabur 借鉴设计 §2.5）：apps diagnose 四
// 形态（健康空态 + unreadable 诚实面 / crash-loop 命中态 / unexplained 兜底
// 态 / 稳态 stopped 的 ADVICE 徽章形态）+ findings list 四形态（全量聚合 /
// 分页首页 / 分页第二页〔健康 App 推进游标不占响应〕/ 组裸形态）。观测注入
// 经 FakeRuntime ReportEvent（终态事实逐实例入账），消费经 Watch 异步——
// 注入后轮询诊断面到条目就位再跑 CLI（golden 确定性）；--json 轮幂等重跑。

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// diagnoseGoldenReady 轮询到诊断面条目就位（观测经 Watch 消费 goroutine
// 异步进缓存/账本——单次注入即查会抢跑；就位门 = 期望条目 id 全在场）。
func diagnoseGoldenReady(t *testing.T, h *apitest.Harness, appID string, want ...string) {
	t.Helper()
	ctx := sdk.WithToken(context.Background(), h.Token)
	client := runtimev1.NewDiagnosisServiceClient(h.Conn)
	require.Eventually(t, func() bool {
		got, err := client.DiagnoseApp(ctx, &runtimev1.DiagnoseAppRequest{AppId: appID})
		if err != nil {
			return false
		}
		have := map[string]bool{}
		for _, f := range got.GetFindings() {
			have[f.GetFindingId()] = true
		}
		for _, w := range want {
			if !have[w] {
				return false
			}
		}
		return true
	}, 5e9, 2e7)
}

// goldenAppSeq 读 App 创建序（findings list 分页游标锚——ListApps 同轴）。
func goldenAppSeq(t *testing.T, h *apitest.Harness, projectID, appID string) int64 {
	t.Helper()
	list, err := structurev1.NewAppsServiceClient(h.Conn).ListApps(
		sdk.WithToken(context.Background(), h.Token),
		&structurev1.ListAppsRequest{ProjectId: projectID})
	require.NoError(t, err)
	for _, a := range list.GetApps() {
		if a.GetId() == appID {
			return a.GetSeq()
		}
	}
	t.Fatalf("app %s not found for the pagination cursor anchor", appID)
	return 0
}

func TestGoldenDiagnosisVerbs(t *testing.T) {
	h := newGoldenHarness(t)

	// 种子：project + 四 App（创建序即分页游标轴）——acrash（崩溃循环）/
	// bhealthy（稳态 running）/ cmystery（单发失败无信号 → unexplained）/
	// dstopped（声明态 stopped → ADVICE）。各部署到 succeeded 后注入观测。
	code, out, stderr := runCLI(t, "projects", "create", "diag")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)
	seed := func(name string) string {
		code, out, stderr := runCLI(t, "apps", "create", "--project", projectID, name)
		if code != 0 || stderr != "" {
			t.Fatalf("seed app %s: code=%d stderr=%q", name, code, stderr)
		}
		appID := extractTailID(out)
		code, _, stderr = runCLI(t, "deploy", "--app", appID, "--image", "nginx:1.27", "--yes")
		if code != 0 || stderr != "" {
			t.Fatalf("seed deploy %s: code=%d stderr=%q", name, code, stderr)
		}
		promoteToSucceeded(t, h, appID)
		return appID
	}
	crashApp := seed("acrash")
	healthyApp := seed("bhealthy")
	mysteryApp := seed("cmystery")
	stoppedApp := seed("dstopped")

	// 观测注入（ReportEvent：终态事实逐实例入账——崩溃循环需逐次实例）：
	// crash-loop = 三次终态（exit 1）；mystery = 单发 failed 无信号；
	// stopped = 声明态 stopped（advice 档）。gen 均为各 App 首笔部署的 1。
	one := 1
	for _, inst := range []string{"task-a", "task-b", "task-c"} {
		h.Runtime.ReportEvent(capability.WorkloadEvent{
			WorkloadID: crashApp + "-web", Generation: capability.Generation(1),
			State: capability.WorkloadFailed, ExitCode: &one,
			Reason: "task: non-zero exit", Instance: inst,
		})
	}
	h.Runtime.ReportEvent(capability.WorkloadEvent{
		WorkloadID: mysteryApp + "-web", Generation: capability.Generation(1),
		State: capability.WorkloadFailed, Instance: "task-x",
	})
	h.Runtime.ReportEvent(capability.WorkloadEvent{
		WorkloadID: stoppedApp + "-web", Generation: capability.Generation(1),
		State: capability.WorkloadStopped, Instance: "task-s",
	})
	diagnoseGoldenReady(t, h, crashApp, "F_WORKLOAD_CRASH_LOOP")
	diagnoseGoldenReady(t, h, mysteryApp, "F_DIAGNOSIS_UNEXPLAINED")
	diagnoseGoldenReady(t, h, stoppedApp, "F_WORKLOAD_STOPPED_EXPLAINED")

	crashSeq := goldenAppSeq(t, h, projectID, crashApp)

	steps := []struct {
		verb string
		args []string
		code int
	}{
		// 组裸形态（findings 组本身也是 --json 契约面）。
		{"findings", []string{"findings"}, 0},
		// 健康空态：空条目集 = 无失败态可解释 + unreadable 诚实面（组件健康
		// 面 B2b 前恒在列）。
		{"apps diagnose healthy", []string{"apps", "diagnose", "GOLDEN_HEALTHY_APP"}, 0},
		// crash-loop 命中态（裸动词名形态）：WARN 徽章 + 证据（state/exit/
		// restarts）+ fix（restart 动词 process 预填 + console 深链）。
		{"apps diagnose", []string{"apps", "diagnose", "GOLDEN_CRASH_APP"}, 0},
		// unexplained 兜底态：无规则信号的最小陈述 + 最近事件证据回指。
		{"apps diagnose unexplained", []string{"apps", "diagnose", "GOLDEN_MYSTERY_APP"}, 0},
		// 稳态 stopped：ADVICE 徽章 + scale fix（replicas 预填）——advice 档
		// 的人类形态锚。
		{"apps diagnose stopped", []string{"apps", "diagnose", "GOLDEN_STOPPED_APP"}, 0},
		// 跨 App 聚合：三 App 有条目（App 创建序），健康 App 不占响应。
		{"findings list", []string{"findings", "list", "--project", "GOLDEN_PROJECT"}, 0},
		// 分页首页（limit=1：仅首 App）。
		{"findings list page", []string{"findings", "list", "--project", "GOLDEN_PROJECT", "--limit", "1"}, 0},
		// 分页第二页（after-app-seq 越过首 App：健康 App 推进游标但不占
		// 响应，落 cmystery 行）。
		{"findings list page after", []string{"findings", "list", "--project", "GOLDEN_PROJECT",
			"--after-app-seq", "GOLDEN_CRASH_SEQ", "--limit", "2"}, 0},
	}

	var jsonRoundCode = map[string]int{} // 全部只读幂等：--json 轮同参直跑

	for _, st := range steps {
		t.Run(st.verb, func(t *testing.T) {
			args := st.args
			for i, a := range args {
				switch a {
				case "GOLDEN_PROJECT":
					args[i] = projectID
				case "GOLDEN_HEALTHY_APP":
					args[i] = healthyApp
				case "GOLDEN_CRASH_APP":
					args[i] = crashApp
				case "GOLDEN_MYSTERY_APP":
					args[i] = mysteryApp
				case "GOLDEN_STOPPED_APP":
					args[i] = stoppedApp
				case "GOLDEN_CRASH_SEQ":
					args[i] = strconv.FormatInt(crashSeq, 10)
				}
			}
			code, out, stderr := runCLI(t, args...)
			if code != st.code {
				t.Fatalf("%s: code=%d stderr=%q args=%q", st.verb, code, stderr, args)
			}
			if st.code == 0 && stderr != "" {
				t.Fatalf("%s: unexpected stderr=%q", st.verb, stderr)
			}
			compareGolden(t, goldenFile(st.verb), normalizeGolden(out))

			// --json 轮：纯读幂等，同参重跑同响应。
			jsonArgs := append([]string{}, args...)
			wantJSONCode := st.code
			if over, ok := jsonRoundCode[st.verb]; ok {
				wantJSONCode = over
			}
			code, out, stderr = runCLI(t, append(jsonArgs, "--json")...)
			if code != wantJSONCode {
				t.Fatalf("%s --json: code=%d want=%d stderr=%q", st.verb, code, wantJSONCode, stderr)
			}
			if wantJSONCode == 0 && stderr != "" {
				t.Fatalf("%s --json: unexpected stderr=%q", st.verb, stderr)
			}
			compareGolden(t, goldenFile(st.verb)+"-json", normalizeGolden(out))
		})
	}
}

// TestDiagnosisVerbShapes 是取参形态的执法面（旗标前置铁律）：apps diagnose
// 位置参数个数错（0/2 个）与 findings list 缺 --project 一律 exit 64——拨号
// 前拒绝，无需夹具（findings list 的多余位置参数入 coverage 守卫表）。
func TestDiagnosisVerbShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"diagnose no app", []string{"apps", "diagnose"}},
		{"diagnose two apps", []string{"apps", "diagnose", "APP1", "APP2"}},
		{"findings list missing project", []string{"findings", "list"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, _ := runCLI(t, tc.args...)
			require.Equal(t, exitUsage, code, "usage error must exit 64: %v", tc.args)
		})
	}
}
