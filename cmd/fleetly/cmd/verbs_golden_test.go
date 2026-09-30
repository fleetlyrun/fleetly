package cmd

// 业务动词 golden 双形态（F0.22 核心）：真 RPC（apitest bufconn 夹具）→
// 输出归一（ULID/指纹/时间戳占位化，fake clock 时间本身确定）→ golden。
// 再生成：go test ./cmd/fleetly/cmd -update。

import (
	"context"
	"regexp"
	"testing"
	"time"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// normalizeGolden 把非确定输出占位化（ULID 主键/短哈希指纹/内容寻址
// digest——digest 由含 ULID 的 spec 派生，逐次不同；fake clock 时间戳
// 确定不占位）。
var (
	ulidRe   = regexp.MustCompile(`[0-9A-HJKMNP-TV-Z]{26}`)
	digestRe = regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	fprRe    = regexp.MustCompile(`\b[0-9a-f]{16}\b`)
)

func normalizeGolden(s string) string {
	s = ulidRe.ReplaceAllString(s, "<ULID>")
	s = digestRe.ReplaceAllString(s, "<DIGEST>")
	s = fprRe.ReplaceAllString(s, "<FP>")
	return s
}

func goldenFile(verb string) string {
	return regexp.MustCompile(`\s+`).ReplaceAllString(verb, "-")
}

// TestGoldenBusinessVerbs 顺序跑完整业务流（同一夹具状态），每动词比较
// 双形态 golden。
func TestGoldenBusinessVerbs(t *testing.T) {
	h := apitest.NewManual(t)
	origDial := dialClient
	dialClient = func(_ string, opts ...fleetly.Option) (*fleetly.Client, error) {
		return fleetly.Dial("passthrough:///bufnet", append(opts, h.DialOpts()...)...)
	}
	t.Cleanup(func() { dialClient = origDial })

	type step struct {
		verb string
		args []string
	}
	steps := []step{
		{"projects create", []string{"projects", "create", "shop"}},
		{"projects list", []string{"projects", "list"}},
		{"apps create", []string{"apps", "create", "--project", "GOLDEN_PROJECT", "web"}},
		{"apps list", []string{"apps", "list", "--project", "GOLDEN_PROJECT"}},
		{"deploy", []string{"deploy", "--app", "GOLDEN_APP", "--image", "nginx:1.27"}},
		{"rollback", []string{"rollback", "--app", "GOLDEN_APP"}},
		{"deployments list", []string{"deployments", "list", "--app", "GOLDEN_APP"}},
		{"revisions list", []string{"revisions", "list", "--app", "GOLDEN_APP"}},
		{"secrets put", []string{"secrets", "put", "--project", "GOLDEN_PROJECT", "--value", "s3cret", "api-token"}},
		{"secrets list", []string{"secrets", "list", "--project", "GOLDEN_PROJECT"}},
		{"configs put", []string{"configs", "put", "--project", "GOLDEN_PROJECT", "--value", "mode=gold", "app.ini"}},
		{"configs list", []string{"configs", "list", "--project", "GOLDEN_PROJECT"}},
		{"volumes create", []string{"volumes", "create", "--project", "GOLDEN_PROJECT", "data"}},
		{"networks create", []string{"networks", "create", "--project", "GOLDEN_PROJECT", "default"}},
		{"routes create", []string{"routes", "create", "--project", "GOLDEN_PROJECT", "--host", "shop.127.0.0.1.sslip.io", "--app", "GOLDEN_APP", "--process", "web", "--port", "8080", "--protocol", "h2c"}},
		{"routes list", []string{"routes", "list"}},
		{"nodes list", []string{"nodes", "list"}},
		{"nodes enroll", []string{"nodes", "enroll"}},
	}

	// GOLDEN_PROJECT/GOLDEN_APP 占位替换为夹具真实 ID（项目 ID 是 ULID，
	// 归一后可预测）。
	var projectID, appID string
	for _, st := range steps {
		t.Run(st.verb, func(t *testing.T) {
			args := st.args
			for i, a := range args {
				if a == "GOLDEN_PROJECT" {
					args[i] = projectID
				}
				if a == "GOLDEN_APP" {
					args[i] = appID
				}
			}
			code, out, stderr := runCLI(t, args...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s: code=%d stderr=%q args=%q", st.verb, code, stderr, args)
			}
			// 捕获后续步骤需要的 ID（人类形态行：created project shop (id X)）。
			if st.verb == "projects create" {
				projectID = extractTailID(out)
			}
			if st.verb == "apps create" {
				appID = extractTailID(out)
			}
			// deploy 后推进到 succeeded（rollback 的 golden 需要成功基线）。
			if st.verb == "deploy" {
				promoteToSucceeded(t, h, appID)
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
			if code != 0 || stderr != "" {
				t.Fatalf("%s --json: code=%d stderr=%q", st.verb, code, stderr)
			}
			compareGolden(t, goldenFile(st.verb)+"-json", normalizeGolden(out))
		})
	}
}

// jsonArgOverrides 是 --json 轮的位置/唯一值替换（索引 → 新值；GOLDEN_*
// 占位在运行时同样被替换）。
var jsonArgOverrides = map[string]map[int]string{
	"projects create": {2: "shop-json"},
	"apps create":     {4: "web-json"},
	"secrets put":     {6: "api-token-json"},
	"configs put":     {6: "app-json.ini"},
	"volumes create":  {4: "data-json"},
	"networks create": {4: "default-json"},
	"routes create":   {5: "json.127.0.0.1.sslip.io"},
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
	ctx := context.Background()
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
