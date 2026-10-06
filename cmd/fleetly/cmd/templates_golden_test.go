package cmd

// templates 命令组 + create-from-dokploy golden（F3.3，ADR-0050）：目录
// 双形态、instantiate 全链（人类形态与手动驱动夹具并发——真等待路径；
// --json 走 --no-wait）、迁移钩子 dry-run 双形态与 skip 诚实报告。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestGoldenTemplatesCatalog(t *testing.T) {
	_ = newGoldenHarness(t)

	// 组本体（无参列出子命令——双形态契约面）。
	code, out, stderr := runCLI(t, "templates")
	if code != 0 || stderr != "" {
		t.Fatalf("templates: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "templates", normalizeGolden(out))
	code, out, stderr = runCLI(t, "templates", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("templates --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "templates-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "templates", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("templates list: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "templates-list", normalizeGolden(out))

	code, out, stderr = runCLI(t, "templates", "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("templates list --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "templates-list-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "templates", "show", "grafana")
	if code != 0 || stderr != "" {
		t.Fatalf("templates show grafana: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "templates-show", normalizeGolden(out))

	code, out, stderr = runCLI(t, "templates", "show", "grafana", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("templates show grafana --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "templates-show-json", normalizeGolden(out))

	code, _, stderr = runCLI(t, "templates", "show", "missing")
	if code == 0 {
		t.Fatal("templates show missing: expected failure")
	}
	compareGolden(t, "templates-show-missing", normalizeGolden(stderr))

	// refresh 未配置目录源：精确拒绝（内嵌目录即全部）——双形态。
	code, _, stderr = runCLI(t, "templates", "refresh")
	if code == 0 {
		t.Fatal("templates refresh: expected failure without a configured catalog url")
	}
	compareGolden(t, "templates-refresh", normalizeGolden(stderr))
	code, _, stderr = runCLI(t, "templates", "refresh", "--json")
	if code == 0 {
		t.Fatal("templates refresh --json: expected failure without a configured catalog url")
	}
	compareGolden(t, "templates-refresh-json", normalizeGolden(stderr))
}

func TestGoldenTemplatesInstantiateWaitsToSucceeded(t *testing.T) {
	h := newGoldenHarness(t)
	ctx := sdk.WithToken(context.Background(), h.Token)

	type result struct {
		code   int
		out    string
		stderr string
	}
	done := make(chan result, 1)
	go func() {
		code, out, stderr := runCLI(t, "templates", "instantiate",
			"--project", "tpl", "--app", "site", "--set", "host=site.127.0.0.1.sslip.io", "nginx")
		done <- result{code, out, stderr}
	}()

	var appID string
	for appID == "" {
		select {
		case r := <-done:
			t.Fatalf("instantiate settled before any deployment appeared: code=%d out=%q stderr=%q", r.code, r.out, r.stderr)
		default:
		}
		row := h.DB.Runner().QueryRowContext(ctx, `SELECT app_id FROM deployments LIMIT 1`)
		_ = row.Scan(&appID)
		time.Sleep(20 * time.Millisecond)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case r := <-done:
			require.Zero(t, r.code, "instantiate: %s", r.stderr)
			compareGolden(t, "templates-instantiate", normalizeGolden(r.out))
			return
		default:
			if time.Now().After(deadline) {
				t.Fatal("instantiate did not settle within the drive budget")
			}
		}
		h.Runtime.ReportRunning(appID+"-web", 1)
		h.Drive(ctx)
		h.Clock.Advance(120 * time.Second)
	}
}

func TestGoldenTemplatesInstantiateNoWaitJSON(t *testing.T) {
	_ = newGoldenHarness(t)
	code, out, stderr := runCLI(t, "templates", "instantiate",
		"--project", "tpl-json", "--app", "dash",
		"--set", "host=dash.127.0.0.1.sslip.io", "--no-wait", "--json", "grafana")
	if code != 0 || stderr != "" {
		t.Fatalf("instantiate --json --no-wait: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "templates-instantiate-json", normalizeGolden(out))

	// 幂等重跑：secret reused（指纹不变由 apitest 承载；golden 钉 created
	// → reused 报告面）。
	code, out, stderr = runCLI(t, "templates", "instantiate",
		"--project", "tpl-json", "--app", "dash",
		"--set", "host=dash.127.0.0.1.sslip.io", "--no-wait", "--json", "grafana")
	if code != 0 || stderr != "" {
		t.Fatalf("instantiate re-run: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "templates-instantiate-json-rerun", normalizeGolden(out))

	// secret 型供值：CLI 先拒（省一轮往返）。
	code, _, stderr = runCLI(t, "templates", "instantiate",
		"--project", "tpl-json", "--app", "dash2",
		"--set", "admin_password=x", "--set", "host=dash2.127.0.0.1.sslip.io", "--no-wait", "grafana")
	if code == 0 {
		t.Fatal("secret variable supplied: expected CLI rejection")
	}
	compareGolden(t, "templates-instantiate-secret-reject", normalizeGolden(stderr))
}

// dokployExportFixture 是迁移钩子夹具：可映射（image app + compose app +
// domains + postgres）与不可映射（git 源 app、mariadb）并存——skip 报告
// 的诚实面。
const dokployExportFixture = `{
  "applications": [
    {"applicationId": "app-1", "name": "landing", "buildType": "dockerimage", "dockerImage": "nginx:1.27", "env": "TITLE=hello"},
    {"applicationId": "app-2", "name": "builder", "buildType": "dockerfile", "repository": "github.com/acme/builder", "env": ""}
  ],
  "compose": [
    {"composeId": "cmp-1", "name": "echo-app", "composeContent": "services:\n  web:\n    image: hashicorp/http-echo:1.0\n    environment:\n      GREETING: ${GREETING}\n    ports: [\"5678\"]\n", "env": "GREETING=hi"},
    {"composeId": "cmp-2", "name": "holey", "composeContent": "services:\n  web:\n    image: nginx:1.27\n    environment:\n      X: ${MISSING}\n", "env": ""}
  ],
  "domains": [
    {"domainId": "dom-1", "host": "landing.127.0.0.1.sslip.io", "port": 80, "applicationId": "app-1", "serviceName": ""},
    {"domainId": "dom-2", "host": "echo.127.0.0.1.sslip.io", "port": 5678, "composeId": "cmp-1", "serviceName": "web"}
  ],
  "databases": [
    {"databaseId": "db-1", "name": "shop", "type": "postgres"},
    {"databaseId": "db-2", "name": "maria", "type": "mariadb"}
  ]
}`

func TestGoldenCreateFromDokploy(t *testing.T) {
	_ = newGoldenHarness(t)
	path := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(path, []byte(dokployExportFixture), 0o600); err != nil {
		t.Fatal(err)
	}

	// dry-run 双形态：计划 + 逐条 skip，零调用（文件名对准动词级双形态
	// 契约——TestAllVerbsHaveDualGoldens）。
	code, out, stderr := runCLI(t, "create-from-dokploy", "--file", path, "--project", "mig", "--dry-run")
	if code != 0 || stderr != "" {
		t.Fatalf("dry-run: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "create-from-dokploy", normalizeGolden(out))

	code, out, stderr = runCLI(t, "create-from-dokploy", "--file", path, "--project", "mig", "--dry-run", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("dry-run --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "create-from-dokploy-json", normalizeGolden(out))

	// 执行态：project/database/app/deployment/route 各就位（FakeRuntime 收
	// 敛环在手动夹具下不推进——部署停在 queued 即递交成功面）。
	code, out, stderr = runCLI(t, "create-from-dokploy", "--file", path, "--project", "mig")
	if code != 0 || stderr != "" {
		t.Fatalf("run: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "dokploy-run", normalizeGolden(out))

	// 重跑收敛：全部 reused。
	code, out, stderr = runCLI(t, "create-from-dokploy", "--file", path, "--project", "mig")
	if code != 0 || stderr != "" {
		t.Fatalf("run re-run: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "dokploy-run-rerun", normalizeGolden(out))
}
