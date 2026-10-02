package cmd

// 业务动词 golden 双形态（F0.22 核心）：真 RPC（apitest bufconn 夹具）→
// 输出归一（ULID/指纹/时间戳占位化，fake clock 时间本身确定）→ golden。
// 再生成：go test ./cmd/fleetly/cmd -update。

import (
	"context"
	"regexp"
	"testing"
	"time"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// normalizeGolden 把非确定输出占位化（ULID 主键/短哈希指纹/内容寻址
// digest——digest 由含 ULID 的 spec 派生，逐次不同；fake clock 时间戳
// 确定不占位；凭证明文随机逐次不同）。
var (
	ulidRe       = regexp.MustCompile(`[0-9A-HJKMNP-TV-Z]{26}`)
	lowerULIDRe  = regexp.MustCompile(`[0-9a-hjkmnp-tv-z]{26}`) // engine 铸名（task-/run- DNS 名）
	digestRe     = regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	fprRe        = regexp.MustCompile(`\b[0-9a-f]{16}\b`)
	tokenRe      = regexp.MustCompile(`flt_[A-Za-z0-9_-]{4,}`)
	invitationRe = regexp.MustCompile(`fltinv_[A-Za-z0-9_-]{4,}`)
	hookTokenRe  = regexp.MustCompile(`flthook_[A-Za-z0-9_-]{4,}`)
	credPathRe   = regexp.MustCompile(`(?m)^credentials saved: .*$`)
	credJSONRe   = regexp.MustCompile(`"credentials_path": ".*?"`)
)

func normalizeGolden(s string) string {
	s = invitationRe.ReplaceAllString(s, "<INVITATION>")
	s = hookTokenRe.ReplaceAllString(s, "<HOOKTOKEN>")
	s = credPathRe.ReplaceAllString(s, "credentials saved: <CREDS>")
	s = credJSONRe.ReplaceAllString(s, `"credentials_path": "<CREDS>"`)
	s = tokenRe.ReplaceAllString(s, "<TOKEN>")
	// digest/指纹先行（完整十六进制串），铸名的小写 ULID 后行——顺序颠倒
	// 会把 digest 中恰符合 ULID 字符集的 26 长段误占位（revisions_list 实证）。
	s = digestRe.ReplaceAllString(s, "<DIGEST>")
	s = fprRe.ReplaceAllString(s, "<FP>")
	s = lowerULIDRe.ReplaceAllString(s, "<ULID>")
	s = ulidRe.ReplaceAllString(s, "<ULID>")
	return s
}

func goldenFile(verb string) string {
	return regexp.MustCompile(`\s+`).ReplaceAllString(verb, "-")
}

// goldenNodeID 是 FakeRuntime 集群观测里的固定平台节点 ID（apitest 夹具
// DescribeCluster 返回值）。
const goldenNodeID = "01JD0NODE00000000000000000"

// TestGoldenBusinessVerbs 顺序跑完整业务流（同一夹具状态），每动词比较
// 双形态 golden。
func TestGoldenBusinessVerbs(t *testing.T) {
	h := newGoldenHarness(t)

	type step struct {
		verb string
		args []string
		// code 是期望退出码（默认 0；diff 类有变化 = 2，stdout 照常比较）。
		code int
	}
	steps := []step{
		{"projects create", []string{"projects", "create", "shop"}, 0},
		{"projects list", []string{"projects", "list"}, 0},
		{"apps create", []string{"apps", "create", "--project", "GOLDEN_PROJECT", "web"}, 0},
		{"apps list", []string{"apps", "list", "--project", "GOLDEN_PROJECT"}, 0},
		{"deploy", []string{"deploy", "--app", "GOLDEN_APP", "--image", "nginx:1.27"}, 0},
		{"rollback", []string{"rollback", "--app", "GOLDEN_APP"}, 0},
		{"deployments list", []string{"deployments", "list", "--app", "GOLDEN_APP"}, 0},
		{"revisions list", []string{"revisions", "list", "--app", "GOLDEN_APP"}, 0},
		// 第二镜像 → R2（与 R1 有字段差）：revisions diff 的有变化形态。
		{"deploy second image", []string{"deploy", "--app", "GOLDEN_APP", "--image", "nginx:1.26"}, 0},
		{"revisions diff", []string{"revisions", "diff", "--app", "GOLDEN_APP", "--from", "1", "--to", "2"}, exitChanges},
		{"builds list", []string{"builds", "list", "--app", "GOLDEN_APP"}, 0},
		{"secrets put", []string{"secrets", "put", "--project", "GOLDEN_PROJECT", "--value", "s3cret", "api-token"}, 0},
		{"secrets list", []string{"secrets", "list", "--project", "GOLDEN_PROJECT"}, 0},
		{"configs put", []string{"configs", "put", "--project", "GOLDEN_PROJECT", "--value", "mode=gold", "app.ini"}, 0},
		{"configs list", []string{"configs", "list", "--project", "GOLDEN_PROJECT"}, 0},
		{"volumes create", []string{"volumes", "create", "--project", "GOLDEN_PROJECT", "data"}, 0},
		{"networks create", []string{"networks", "create", "--project", "GOLDEN_PROJECT", "default"}, 0},
		{"routes create", []string{"routes", "create", "--project", "GOLDEN_PROJECT", "--host", "shop.127.0.0.1.sslip.io", "--app", "GOLDEN_APP", "--process", "web", "--port", "8080", "--protocol", "h2c"}, 0},
		{"routes list", []string{"routes", "list"}, 0},

		// 跨 Project peer 声明链（F1.8，ADR-0013 附录 A）：接收方项目建网 →
		// 挂靠方 declare（幂等键——--json 轮重放同响应）→ 接收方 approve →
		// 双侧视图 list → revoke（幂等）。置于 events follow 前：重放面覆盖
		// peer 三拍事件。
		{"projects create messaging", []string{"projects", "create", "messaging"}, 0},
		{"networks create messaging", []string{"networks", "create", "--project", "GOLDEN_PROJECT2", "bus"}, 0},
		{"networks declare", []string{"networks", "declare", "--network", "GOLDEN_NETWORK", "--project", "GOLDEN_PROJECT", "--idempotency-key", "peer-declare"}, 0},
		{"networks approve", []string{"networks", "approve", "GOLDEN_PEER"}, 0},
		{"networks peers", []string{"networks", "peers", "--project", "GOLDEN_PROJECT"}, 0},
		{"networks revoke", []string{"networks", "revoke", "GOLDEN_PEER"}, 0},
		// 事件订阅面的有界形态（F1.2）：--replay 重放保留窗后退出（follow
		// 无界不进 golden）。
		{"events follow", []string{"events", "follow", "--replay"}, 0},
		{"nodes list", []string{"nodes", "list"}, 0},
		{"nodes enroll", []string{"nodes", "enroll"}, 0},
		// 节点运维三动词（F0.19 RuntimeAdmin 面）：FakeRuntime 集群里的固定
		// 平台节点 ID。含 'O'（非 ULID 字符）不进归一，golden 逐字确定。
		{"nodes drain", []string{"nodes", "drain", "--node", goldenNodeID}, 0},
		{"nodes cordon", []string{"nodes", "cordon", "--node", goldenNodeID}, 0},
		{"nodes uncordon", []string{"nodes", "uncordon", "--node", goldenNodeID}, 0},

		// Automation 动词（F1.5/F1.6）：one-shot 全链 + resident 池。
		{"tasks create", []string{"tasks", "create", "--project", "GOLDEN_PROJECT", "--name", "migrate",
			"--image", "busybox:1.37", "--network-group", "dispatcher", "--env", "POOL=gold", "--ttl-seconds", "3600"}, 0},
		{"tasks list", []string{"tasks", "list", "--project", "GOLDEN_PROJECT"}, 0},
		{"tasks get", []string{"tasks", "get", "--task", "GOLDEN_TASK"}, 0},
		{"runs list", []string{"runs", "list", "--task", "GOLDEN_TASK"}, 0},
		{"runs get", []string{"runs", "get", "--run", "GOLDEN_RUN"}, 0},
		{"runs stop", []string{"runs", "stop", "--run", "GOLDEN_RUN"}, 0},
		{"runs wait", []string{"runs", "wait", "--run", "GOLDEN_RUN"}, 0},
		{"tasks create resident", []string{"tasks", "create", "--project", "GOLDEN_PROJECT", "--name", "dispatcher",
			"--form", "resident", "--image", "ghcr.io/torchwood/dispatcher:1", "--concurrency", "2"}, 0},
		{"tasks scale", []string{"tasks", "scale", "--task", "GOLDEN_TASK2", "--concurrency", "3"}, 0},
		{"tasks renew", []string{"tasks", "renew", "--task", "GOLDEN_TASK2"}, 0},
		{"tasks stop", []string{"tasks", "stop", "--task", "GOLDEN_TASK2", "--force"}, 0},
		{"tasks delete", []string{"tasks", "delete", "--task", "GOLDEN_TASK"}, 0},
		{"tasks delete resident", []string{"tasks", "delete", "--task", "GOLDEN_TASK2"}, 0},

		// Automation 动词（F1.7）：Schedule 时区 cron——create（东京 12:00 =
		// UTC 03:00，fake 时钟已推过若干分钟不影响当日拍点）→ list/get →
		// trigger（立即铸 Task；下一拍不动）→ delete。
		{"schedules create", []string{"schedules", "create", "--project", "GOLDEN_PROJECT", "--name", "nightly-report",
			"--cron", "0 12 * * *", "--timezone", "Asia/Tokyo", "--image", "busybox:1.37", "--ttl-seconds", "3600"}, 0},
		{"schedules list", []string{"schedules", "list", "--project", "GOLDEN_PROJECT"}, 0},
		{"schedules get", []string{"schedules", "get", "--schedule", "GOLDEN_SCHEDULE"}, 0},
		{"schedules trigger", []string{"schedules", "trigger", "--schedule", "GOLDEN_SCHEDULE"}, 0},
		{"schedules delete", []string{"schedules", "delete", "--schedule", "GOLDEN_SCHEDULE"}, 0},

		// Governance 动词（F1.9）：freeze set（全局；幂等键让 --json 轮重放
		// 同响应——同键再 Set 会撞活跃冻结唯一索引，declare 同款手法）→
		// list（active）→ lift → list（历史行可见）。冻结期拒绝面的断言在
		// apitest（CLI 错误信封含随机 error_id，不可 golden）。
		{"freeze set", []string{"freeze", "set", "--all", "--reason", "golden maintenance", "--idempotency-key", "freeze-set"}, 0},
		{"freeze list", []string{"freeze", "list"}, 0},
		{"freeze lift", []string{"freeze", "lift", "GOLDEN_FREEZE"}, 0},
		{"freeze list after lift", []string{"freeze", "list"}, 0},
	}

	// GOLDEN_PROJECT/GOLDEN_APP 占位替换为夹具真实 ID（项目 ID 是 ULID，
	// 归一后可预测）。
	var projectID, project2ID, networkID, peerID, appID, taskID, task2ID, runID, scheduleID, freezeID string
	for _, st := range steps {
		t.Run(st.verb, func(t *testing.T) {
			args := st.args
			for i, a := range args {
				if a == "GOLDEN_PROJECT" {
					args[i] = projectID
				}
				if a == "GOLDEN_PROJECT2" {
					args[i] = project2ID
				}
				if a == "GOLDEN_NETWORK" {
					args[i] = networkID
				}
				if a == "GOLDEN_PEER" {
					args[i] = peerID
				}
				if a == "GOLDEN_APP" {
					args[i] = appID
				}
				if a == "GOLDEN_TASK" {
					args[i] = taskID
				}
				if a == "GOLDEN_TASK2" {
					args[i] = task2ID
				}
				if a == "GOLDEN_RUN" {
					args[i] = runID
				}
				if a == "GOLDEN_SCHEDULE" {
					args[i] = scheduleID
				}
				if a == "GOLDEN_FREEZE" {
					args[i] = freezeID
				}
			}
			code, out, stderr := runCLI(t, args...)
			wantStderr := ""
			if st.code == exitChanges {
				wantStderr = "\n" // errChanges 渲染为空，框架打一行换行
			}
			if code != st.code || stderr != wantStderr {
				t.Fatalf("%s: code=%d stderr=%q args=%q", st.verb, code, stderr, args)
			}
			// 捕获后续步骤需要的 ID（人类形态行：created project shop (id X)）。
			if st.verb == "projects create" {
				projectID = extractTailID(out)
			}
			if st.verb == "projects create messaging" {
				project2ID = extractTailID(out)
			}
			if st.verb == "networks create messaging" {
				networkID = extractTailID(out)
			}
			if st.verb == "networks declare" {
				peerID = extractPeerID(t, out)
			}
			if st.verb == "apps create" {
				appID = extractTailID(out)
			}
			if st.verb == "tasks create" {
				taskID = extractTaskID(t, out)
				spawnTaskRun(t, h, taskID)
				runID = firstRunID(t, h, taskID)
			}
			if st.verb == "tasks create resident" {
				task2ID = extractTaskID(t, out)
				h.Drive(sdk.WithToken(context.Background(), h.Token))
			}
			if st.verb == "schedules create" {
				scheduleID = extractScheduleID(t, out)
			}
			if st.verb == "freeze set" {
				freezeID = extractTailID(out)
			}
			// deploy 后推进到 succeeded（rollback 的 golden 需要成功基线）。
			if st.verb == "deploy" {
				promoteToSucceeded(t, h, appID)
			}
			// runs stop 后收口到终态（runs wait 的前置：WaitRun 首帧即终态）。
			if st.verb == "runs stop" {
				h.Runtime.ReportStopped(runID, 1)
				h.Drive(sdk.WithToken(context.Background(), h.Token))
			}
			// resident 池 stop --force 后注入停止观测（不驱动到 drained——
			// --json 轮的 StopTask 需 draining 非终态；删除步不受影响）。
			if st.verb == "tasks stop" {
				for _, r := range taskRunIDs(t, h, task2ID) {
					h.Runtime.ReportStopped(r, 1)
				}
			}
			compareGolden(t, goldenFile(st.verb), normalizeGolden(out))

			// --json 轮：create/put 类换名字撞唯一名；list/diff 类幂等直跑。
			jsonArgs := append([]string{}, args...)
			if over, ok := jsonArgOverrides[st.verb]; ok {
				for i, v := range over {
					jsonArgs[i] = v
				}
			}
			code, out, stderr = runCLI(t, append(jsonArgs, "--json")...)
			if code != st.code || stderr != wantStderr {
				t.Fatalf("%s --json: code=%d stderr=%q", st.verb, code, stderr)
			}
			compareGolden(t, goldenFile(st.verb)+"-json", normalizeGolden(out))
		})
	}
}

// jsonArgOverrides 是 --json 轮的位置/唯一值替换（索引 → 新值；GOLDEN_*
// 占位在运行时同样被替换）。
var jsonArgOverrides = map[string]map[int]string{
	"projects create":           {2: "shop-json"},
	"projects create messaging": {2: "messaging-json"},
	"apps create":               {4: "web-json"},
	"secrets put":               {6: "api-token-json"},
	"configs put":               {6: "app-json.ini"},
	"volumes create":            {4: "data-json"},
	"networks create":           {4: "default-json"},
	"networks create messaging": {4: "bus-json"},
	"routes create":             {5: "json.127.0.0.1.sslip.io"},
	"tasks create":              {5: "migrate-json"},
	"tasks create resident":     {5: "dispatcher-json"},
	"schedules create":          {5: "nightly-report-json"},
}

// extractTailID 取 "... (id X)" 尾部的 ID。
var tailIDRe = regexp.MustCompile(`\(id ([0-9A-HJKMNP-TV-Z]{26})\)`)

func extractTailID(out string) string {
	m := tailIDRe.FindStringSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// promoteToSucceeded 手动驱动到 succeeded（观测注入 + 假时钟推过 L1/L3；
// rollback 的成功基线前置——手动形态保证 golden 确定性）。
func promoteToSucceeded(t *testing.T, h *apitest.Harness, appID string) {
	t.Helper()
	client := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	ctx := sdk.WithToken(context.Background(), h.Token) // 执法链激活后读路径同样要凭证
	for i := 0; i < 20; i++ {
		h.Runtime.ReportRunning(appID+"-web", 1)
		h.Drive(ctx)
		h.Clock.Advance(120 * time.Second)
		h.Drive(ctx)
		list, err := client.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
		if err == nil && len(list.GetDeployments()) > 0 && list.GetDeployments()[0].GetState() == "succeeded" {
			return
		}
	}
	t.Fatal("deployment did not reach succeeded within the manual drive budget")
}

// taskCreatedRe 取人类形态 "task X created (...)" 的 ID。
var taskCreatedRe = regexp.MustCompile(`task ([0-9A-HJKMNP-TV-Z]{26}) created`)

func extractTaskID(t *testing.T, out string) string {
	t.Helper()
	m := taskCreatedRe.FindStringSubmatch(out)
	if len(m) < 2 {
		t.Fatalf("cannot extract task id from output: %q", out)
	}
	return m[1]
}

// scheduleCreatedRe 取人类形态 "schedule X created (...)" 的 ID。
var scheduleCreatedRe = regexp.MustCompile(`schedule ([0-9A-HJKMNP-TV-Z]{26}) created`)

func extractScheduleID(t *testing.T, out string) string {
	t.Helper()
	m := scheduleCreatedRe.FindStringSubmatch(out)
	if len(m) < 2 {
		t.Fatalf("cannot extract schedule id from output: %q", out)
	}
	return m[1]
}

// peerDeclaredRe 取人类形态 "declared peer X on network ..." 的 ID。
var peerDeclaredRe = regexp.MustCompile(`declared peer ([0-9A-HJKMNP-TV-Z]{26}) on network`)

func extractPeerID(t *testing.T, out string) string {
	t.Helper()
	m := peerDeclaredRe.FindStringSubmatch(out)
	if len(m) < 2 {
		t.Fatalf("cannot extract peer id from output: %q", out)
	}
	return m[1]
}

// spawnTaskRun 驱动补足 + 观测 running（后续 golden 步骤的状态确定性）。
func spawnTaskRun(t *testing.T, h *apitest.Harness, taskID string) {
	t.Helper()
	ctx := sdk.WithToken(context.Background(), h.Token)
	h.Drive(ctx)
	for _, r := range taskRunIDs(t, h, taskID) {
		h.Runtime.ReportRunning(r, 1)
	}
	h.Drive(ctx)
}

// taskRunIDs 返回 Task 名下全部 Run 的 ID。
func taskRunIDs(t *testing.T, h *apitest.Harness, taskID string) []string {
	t.Helper()
	rc := automationv1.NewRunsServiceClient(h.Conn)
	resp, err := rc.ListRuns(sdk.WithToken(context.Background(), h.Token), &automationv1.ListRunsRequest{TaskId: taskID})
	if err != nil {
		t.Fatalf("list runs for golden fixture: %v", err)
	}
	var ids []string
	for _, r := range resp.GetRuns() {
		ids = append(ids, r.GetId())
	}
	return ids
}

// firstRunID 返回首条 Run ID（新→旧）。
func firstRunID(t *testing.T, h *apitest.Harness, taskID string) string {
	t.Helper()
	ids := taskRunIDs(t, h, taskID)
	if len(ids) == 0 {
		t.Fatalf("no runs spawned for task %s", taskID)
	}
	return ids[0]
}
