package cmd

// B1 W3 确认三形态 golden（设计稿 2026-10-30 §1.6）：deploy / apps delete
// 的（交互预览形态 / --yes / --preview 退出 / --ticket 成功 / 三拒绝形态）
// + rollback 的（--yes / --ticket 拒绝 / 裸调用指引）+ apps scale 双形态。
// 交互形态经 confirmIsTTY/confirmReadAnswer 接缝伪 TTY（设计稿钦点的
// 「伪 TTY 或抽打印函数测」取前者——全链路含拨号与 RPC 都走真路径）。
//
// 再生成：go test ./cmd/fleetly/cmd -update。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// normalizeConfirmGolden：共享 normalizeGolden 先行（hex digest/指纹等族
// 照常占位），再把其 43 字符 token 族的占位（与 browse 票据同形、先到
// 先得）换名为 confirm 票——本测试面唯一 43 字符随机源是 Confirm Ticket。
func normalizeConfirmGolden(s string) string {
	return strings.ReplaceAll(normalizeGolden(s), "<BROWSETICKET>", "<CONFIRMTICKET>")
}

// fakeConfirmTTY 伪 TTY + canned 应答（golden 的交互形态驱动面）。
func fakeConfirmTTY(t *testing.T, answer string) {
	t.Helper()
	origTTY, origRead := confirmIsTTY, confirmReadAnswer
	confirmIsTTY = func() bool { return true }
	confirmReadAnswer = func() (string, error) { return answer, nil }
	t.Cleanup(func() { confirmIsTTY, confirmReadAnswer = origTTY, origRead })
}

// forceNoTTY 钉死非 TTY（拒绝腿不依赖 go test 的 stdin 环境事实）。
func forceNoTTY(t *testing.T) {
	t.Helper()
	orig := confirmIsTTY
	confirmIsTTY = func() bool { return false }
	t.Cleanup(func() { confirmIsTTY = orig })
}

// seedConfirmApp 建独立 App 并以 --yes 部署基线到 succeeded（每腿独立
// App：免遭彼此在途部署的 admission 合并干扰）。
func seedConfirmApp(t *testing.T, h *apitest.Harness, projectID, name string) string {
	t.Helper()
	code, out, stderr := runCLI(t, "apps", "create", "--project", projectID, name)
	require.Zero(t, code, "seed app %s: %s", name, stderr)
	appID := extractTailID(out)
	require.NotEmpty(t, appID, "seed app %s: no id in %q", name, out)
	code, _, stderr = runCLI(t, "deploy", "--app", appID, "--image", "nginx:1.27", "--yes")
	require.Zero(t, code, "seed deploy %s: %s", name, stderr)
	promoteToSucceeded(t, h, appID)
	return appID
}

// seedConfirmAppWithVolume 建独立 App 并以 spec_file（挂卷）部署基线到
// succeeded——拆除预演腿的深形态夹具（保留清单 VOLUME 腿的引用面）。
func seedConfirmAppWithVolume(t *testing.T, h *apitest.Harness, projectID, name string) string {
	t.Helper()
	code, out, stderr := runCLI(t, "apps", "create", "--project", projectID, name)
	require.Zero(t, code, "seed app %s: %s", name, stderr)
	appID := extractTailID(out)
	_, err := structurev1.NewVolumesServiceClient(h.Conn).CreateVolume(h.Ctx(), &structurev1.CreateVolumeRequest{ProjectId: projectID, Name: "scratch-" + name})
	require.NoError(t, err)
	spec := fmt.Sprintf(`{"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27","volumes":[{"volume_id":%q,"target":"/data"}]}]}`, "scratch-"+name)
	specFile := filepath.Join(t.TempDir(), "appspec.json")
	require.NoError(t, os.WriteFile(specFile, []byte(spec), 0o600))
	code, _, stderr = runCLI(t, "deploy", "--app", appID, "--spec-file", specFile, "--yes")
	require.Zero(t, code, "seed spec deploy %s: %s", name, stderr)
	promoteToSucceeded(t, h, appID)
	return appID
}

// mintDeployTicket 经真铸票面（PreviewDeploy RPC）铸一枚绑定当前世界的
// deploy 票——--ticket 腿的两调用流第 1 步（不经 CLI 输出解析，票面直取）。
func mintDeployTicket(t *testing.T, h *apitest.Harness, appID, image string) string {
	t.Helper()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	resp, err := dc.PreviewDeploy(h.Ctx(), &deliveryv1.PreviewDeployRequest{Deploy: &deliveryv1.DeployRequest{
		AppId: appID, Image: image, ProcessName: "web",
	}})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetTicket())
	return resp.GetTicket()
}

// mintTeardownTicket 经 PreviewTeardown 铸拆除票。
func mintTeardownTicket(t *testing.T, h *apitest.Harness, appID string) string {
	t.Helper()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	resp, err := dc.PreviewTeardown(h.Ctx(), &deliveryv1.PreviewTeardownRequest{AppId: appID})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetTicket())
	return resp.GetTicket()
}

// dualForm 双形态跑（json 轮附加 --json），各自 golden（退出码 0 契约）。
func dualForm(t *testing.T, name string, args []string) {
	t.Helper()
	t.Run("human", func(t *testing.T) {
		code, out, stderr := runCLI(t, args...)
		require.Zero(t, code, "%s: %s", name, stderr)
		compareGolden(t, goldenFile(name), normalizeConfirmGolden(out))
	})
	t.Run("json", func(t *testing.T) {
		code, out, stderr := runCLI(t, append(append([]string{}, args...), "--json")...)
		require.Zero(t, code, "%s --json: %s", name, stderr)
		compareGolden(t, goldenFile(name)+"-json", normalizeConfirmGolden(out))
	})
}

// dualFormRound 双形态跑、每轮重建世界（单发腿：删除/烧票后 --json 轮
// 无法重放——app/票每轮现铸〔label 是轮名，app 命名唯一化用〕，输出仍
// 各自 golden）。
func dualFormRound(t *testing.T, name string, argsFor func(label string) []string) {
	t.Helper()
	t.Run("human", func(t *testing.T) {
		code, out, stderr := runCLI(t, argsFor("human")...)
		require.Zero(t, code, "%s: %s", name, stderr)
		compareGolden(t, goldenFile(name), normalizeConfirmGolden(out))
	})
	t.Run("json", func(t *testing.T) {
		code, out, stderr := runCLI(t, append(argsFor("json"), "--json")...)
		require.Zero(t, code, "%s --json: %s", name, stderr)
		compareGolden(t, goldenFile(name)+"-json", normalizeConfirmGolden(out))
	})
}

// dualFormErr 双形态跑错误腿：stdout 须空（不发请求/拒绝无内容面），
// stderr 走 golden。
func dualFormErr(t *testing.T, name string, args []string, wantCode int) {
	t.Helper()
	run := func(t *testing.T, json bool) {
		t.Helper()
		args := append([]string{}, args...)
		if json {
			args = append(args, "--json")
		}
		code, out, stderr := runCLI(t, args...)
		require.Equal(t, wantCode, code, "%s: code (json=%t)", name, json)
		require.Empty(t, out, "%s: stdout must stay empty on the refusal leg", name)
		suffix := ""
		if json {
			suffix = "-json"
		}
		compareGolden(t, goldenFile(name)+suffix, normalizeConfirmGolden(stderr))
	}
	t.Run("human", func(t *testing.T) { run(t, false) })
	t.Run("json", func(t *testing.T) { run(t, true) })
}

// putConfirmAppVariable 改 App 变量（STALE 腿的世界漂移注入：变量环境
// 摘要是 base 多锚之一，设计稿 §1.4 #7）。
func putConfirmAppVariable(t *testing.T, h *apitest.Harness, appID, name, value string) {
	t.Helper()
	_, err := structurev1.NewAppVariablesServiceClient(h.Conn).PutAppVariable(h.Ctx(), &structurev1.PutAppVariableRequest{
		AppId: appID, Name: name, Value: value,
	})
	require.NoError(t, err)
}

// TestGoldenConfirmForms 三动词确认三形态 + 三拒绝呈现。
func TestGoldenConfirmForms(t *testing.T) {
	h := newGoldenHarness(t)
	ctx := h.Ctx()

	code, out, stderr := runCLI(t, "projects", "create", "confirm")
	require.Zero(t, code, "seed project: %s", stderr)
	projectID := extractTailID(out)

	// ---- 拒绝形态：非 TTY 无旗标（= REQUIRED 的 CLI 呈现：本地指引，
	// 不发请求）与 rollback 无预览面腿。----
	t.Run("deploy non-tty refusal", func(t *testing.T) {
		forceNoTTY(t)
		appID := seedConfirmApp(t, h, projectID, "refuse-deploy")
		dualFormErr(t, "deploy confirm-required", []string{"deploy", "--app", appID, "--image", "nginx:1.31"}, 1)
	})
	t.Run("apps delete non-tty refusal", func(t *testing.T) {
		forceNoTTY(t)
		appID := seedConfirmApp(t, h, projectID, "refuse-delete")
		dualFormErr(t, "apps-delete confirm-required", []string{"apps", "delete", "--app", appID}, 1)
	})
	t.Run("rollback refusal (no preview face yet)", func(t *testing.T) {
		forceNoTTY(t)
		appID := seedConfirmApp(t, h, projectID, "refuse-rollback")
		dualFormErr(t, "rollback confirm-required", []string{"rollback", "--app", appID}, 1)
		// TTY 也同形：rollback 无铸票面，交互确认后无票可带——结构性
		// 拒绝与 TTY 无关。
		fakeConfirmTTY(t, "y\n")
		c, o, e := runCLI(t, "rollback", "--app", appID)
		require.Equal(t, 1, c, "tty rollback refusal: %s", e)
		require.Empty(t, o)
		require.Contains(t, e, "requires confirmation but no preview face exists")
	})

	// ---- 旗标互斥（usage 面，exit 64）。----
	t.Run("flag exclusivity", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "mutex")
		for _, args := range [][]string{
			{"deploy", "--app", appID, "--image", "nginx:1.31", "--yes", "--preview"},
			{"deploy", "--app", appID, "--image", "nginx:1.31", "--yes", "--ticket", "t"},
			{"deploy", "--app", appID, "--image", "nginx:1.31", "--preview", "--wait"},
			{"apps", "delete", "--app", appID, "--preview", "--ticket", "t"},
		} {
			c, _, _ := runCLI(t, args...)
			require.Equal(t, exitUsage, c, "%v must be a usage error (flag exclusivity)", args)
		}
	})

	// ---- --yes：skip 直发 + 一行说明（人类形态）。----
	t.Run("deploy yes", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "yes-deploy")
		dualForm(t, "deploy yes", []string{"deploy", "--app", appID, "--image", "nginx:1.31", "--yes"})
	})
	t.Run("rollback yes", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "yes-rollback")
		dualForm(t, "rollback yes", []string{"rollback", "--app", appID, "--yes"})
	})
	t.Run("apps delete yes", func(t *testing.T) {
		dualFormRound(t, "apps-delete yes", func(label string) []string {
			appID := seedConfirmApp(t, h, projectID, "yes-delete-"+label)
			return []string{"apps", "delete", "--app", appID, "--yes"}
		})
	})

	// ---- --preview：打印预览 + ticket 行后退出（两调用流第 1 步）。----
	t.Run("deploy preview", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "preview-deploy")
		dualForm(t, "deploy preview", []string{"deploy", "--app", appID, "--image", "nginx:1.31", "--preview"})
	})
	t.Run("apps delete preview", func(t *testing.T) {
		// 深形态夹具：基线（spec_file 挂卷）+ hook 行 + route 行——级联
		// 三族 + 保留清单（volume）全登场（w2a apitest 同款拓扑）。
		appID := seedConfirmAppWithVolume(t, h, projectID, "preview-delete")
		_, err := deliveryv1.NewHooksServiceClient(h.Conn).SetGitHook(ctx, &deliveryv1.SetGitHookRequest{
			AppId: appID, Repo: "https://github.com/acme/confirm-preview.git",
		})
		require.NoError(t, err)
		_, err = proxyv1.NewRoutesServiceClient(h.Conn).CreateRoute(ctx, &proxyv1.CreateRouteRequest{
			ProjectId: projectID, Host: "preview.confirm.test", AppId: appID, Process: "web", Port: 8000,
		})
		require.NoError(t, err)
		dualForm(t, "apps-delete preview", []string{"apps", "delete", "--app", appID, "--preview"})
	})

	// ---- --ticket：带票直发（两调用流第 2 步；票经真铸票面铸出）。----
	t.Run("deploy ticket", func(t *testing.T) {
		dualFormRound(t, "deploy ticket", func(label string) []string {
			appID := seedConfirmApp(t, h, projectID, "ticket-deploy-"+label)
			return []string{"deploy", "--app", appID, "--image", "nginx:1.31", "--ticket", mintDeployTicket(t, h, appID, "nginx:1.31")}
		})
	})
	t.Run("apps delete ticket", func(t *testing.T) {
		dualFormRound(t, "apps-delete ticket", func(label string) []string {
			appID := seedConfirmApp(t, h, projectID, "ticket-delete-"+label)
			return []string{"apps", "delete", "--app", appID, "--ticket", mintTeardownTicket(t, h, appID)}
		})
	})

	// ---- 三拒绝的服务端呈现：STALE / MISMATCH（REQUIRED = 上文本地腿）。----
	t.Run("deploy ticket stale", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "stale-deploy")
		ticket := mintDeployTicket(t, h, appID, "nginx:1.31")
		putConfirmAppVariable(t, h, appID, "feature_flag", "on")
		dualFormErr(t, "deploy ticket-stale", []string{"deploy", "--app", appID, "--image", "nginx:1.31", "--ticket", ticket}, 1)
	})
	t.Run("deploy ticket mismatch", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "mismatch-deploy")
		dualFormErr(t, "deploy ticket-mismatch", []string{"deploy", "--app", appID, "--image", "nginx:1.31", "--ticket", "bogus-ticket"}, 1)
	})
	t.Run("apps delete ticket stale", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "stale-delete")
		// 铸票前先建一条 route：route 集锚非空才可比对（空集 = 缺席锚，
		// W2a 的缺席比对语义）。
		_, err := proxyv1.NewRoutesServiceClient(h.Conn).CreateRoute(ctx, &proxyv1.CreateRouteRequest{
			ProjectId: projectID, Host: "early.confirm.test", AppId: appID, Process: "web", Port: 8000,
		})
		require.NoError(t, err)
		ticket := mintTeardownTicket(t, h, appID)
		// route 集锚漂移（teardown 票三锚之一）：预览后新增 route——
		// 清单从未展示的删除被拒绝（设计稿 §1.4 #8）。
		_, err = proxyv1.NewRoutesServiceClient(h.Conn).CreateRoute(ctx, &proxyv1.CreateRouteRequest{
			ProjectId: projectID, Host: "late.confirm.test", AppId: appID, Process: "web", Port: 8000,
		})
		require.NoError(t, err)
		dualFormErr(t, "apps-delete ticket-stale", []string{"apps", "delete", "--app", appID, "--ticket", ticket}, 1)
	})
	t.Run("rollback ticket mismatch", func(t *testing.T) {
		// rollback 的 --ticket 通道在场（B2a 修护动词铸票面的前向兼容），
		// 当前唯一可达腿是伪造票拒绝。
		appID := seedConfirmApp(t, h, projectID, "mismatch-rollback")
		dualFormErr(t, "rollback ticket-mismatch", []string{"rollback", "--app", appID, "--ticket", "bogus-ticket"}, 1)
	})

	// ---- 交互形态（伪 TTY）：预览打印 + y/N 提示 + y 带票直发。----
	t.Run("deploy interactive confirm", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "tty-deploy")
		fakeConfirmTTY(t, "y\n")
		dualForm(t, "deploy interactive", []string{"deploy", "--app", appID, "--image", "nginx:1.31"})
	})
	t.Run("deploy interactive decline", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "tty-deploy-decline")
		fakeConfirmTTY(t, "n\n")
		dualForm(t, "deploy interactive-decline", []string{"deploy", "--app", appID, "--image", "nginx:1.31"})
	})
	t.Run("apps delete interactive confirm", func(t *testing.T) {
		fakeConfirmTTY(t, "y\n")
		dualFormRound(t, "apps-delete interactive", func(label string) []string {
			appID := seedConfirmApp(t, h, projectID, "tty-delete-"+label)
			return []string{"apps", "delete", "--app", appID}
		})
	})
}

// TestGoldenAppsScale 双形态 + --wait 并发驱动形态 + 未知进程/非法副本数
// 拒绝面（B1s：ScaleProcess 的 CLI 皮）。
func TestGoldenAppsScale(t *testing.T) {
	h := newGoldenHarness(t)
	code, out, stderr := runCLI(t, "projects", "create", "scale")
	require.Zero(t, code, "seed project: %s", stderr)
	projectID := extractTailID(out)

	t.Run("dual form", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "scale-basic")
		dualForm(t, "apps scale", []string{"apps", "scale", appID, "web", "2"})
	})

	t.Run("wait streams to succeeded", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "scale-wait")
		driveCtx := sdk.WithToken(context.Background(), h.Token)
		type result struct {
			code   int
			out    string
			stderr string
		}
		done := make(chan result, 1)
		go func() {
			c, o, e := runCLI(t, "apps", "scale", "--wait", appID, "web", "2")
			done <- result{c, o, e}
		}()
		// 只读轮询等待 scale 部署现身，随后按 Ensure 回执的代次上报
		// running（quickstart golden 的并发驱动先例；假时钟只在
		// observing 落定后推进一次——S18b 纪律）。
		deadline := time.Now().Add(30 * time.Second)
		advanced := false
		for {
			select {
			case r := <-done:
				require.Zero(t, r.code, "apps scale --wait: %s", r.stderr)
				compareGolden(t, goldenFile("apps scale wait"), normalizeConfirmGolden(r.out))
				return
			default:
				if time.Now().After(deadline) {
					t.Fatal("apps scale --wait did not settle within the drive budget")
				}
			}
			if calls := h.Runtime.Calls(); len(calls) > 0 {
				last := calls[len(calls)-1]
				if w, ok := last.Spec["web"]; ok && w.Replicas == 2 {
					h.Runtime.ReportRunning(w.ID, last.Gen)
				}
			}
			h.Drive(driveCtx)
			if !advanced && latestDeploymentState(t, h, appID) == "observing" {
				h.Clock.Advance(120 * time.Second)
				advanced = true
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("unknown process refuses", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "scale-unknown")
		dualFormErr(t, "apps scale unknown-process", []string{"apps", "scale", appID, "nope", "1"}, 1)
	})

	t.Run("negative replicas is a usage error", func(t *testing.T) {
		appID := seedConfirmApp(t, h, projectID, "scale-bad")
		c, _, _ := runCLI(t, "apps", "scale", appID, "web", "-1")
		require.Equal(t, exitUsage, c)
		c, _, _ = runCLI(t, "apps", "scale", appID, "web")
		require.Equal(t, exitUsage, c)
	})
}
