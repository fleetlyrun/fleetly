package cmd

// databases 命令组 golden（F1.12，ADR-0029）：create/list/get/delete 双形
// 态；独立项目夹具（含网络——数据库可达性前置），删除后卷与凭证 Secret
// 残留的口径在 apitest 承载。

import (
	"context"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// seedDatabaseProject 建项目并返回 projectID。default 网络随项目出生
// （F-C：出生即建行），无需再显式播种——数据库可达性前置已由出生面保证。
func seedDatabaseProject(t *testing.T, name string) string {
	t.Helper()
	code, out, stderr := runCLI(t, "projects", "create", name)
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	return extractTailID(out)
}

// createDatabase 建库并返回 (dbID, 人类形态输出)。
func createDatabase(t *testing.T, projectID, engine, name string, jsonOut bool) (string, string) {
	t.Helper()
	args := []string{"databases", "create", "--project", projectID, "--engine", engine, name}
	if jsonOut {
		args = append(args, "--json")
	}
	code, out, stderr := runCLI(t, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("databases create (%v): code=%d stderr=%q", args, code, stderr)
	}
	return extractTailID(out), out
}

func TestGoldenDatabasesLifecycle(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "dbs")

	dbID, out := createDatabase(t, projectID, "postgres", "shop", false)
	compareGolden(t, "databases-create", normalizeGolden(out))
	_, out = createDatabase(t, projectID, "redis", "cache", true)
	compareGolden(t, "databases-create-json", normalizeGolden(out))

	code, out, stderr := runCLI(t, "databases", "list", "--project", projectID)
	if code != 0 || stderr != "" {
		t.Fatalf("databases list: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-list", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "list", "--project", projectID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("databases list --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-list-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "get", dbID)
	if code != 0 || stderr != "" {
		t.Fatalf("databases get: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-get", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "get", dbID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("databases get --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-get-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "delete", dbID)
	if code != 0 || stderr != "" {
		t.Fatalf("databases delete: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-delete", normalizeGolden(out))

	// --json 删除轮：人类形态另建（id 可提取）再以 --json 删。
	secondID, _ := createDatabase(t, projectID, "postgres", "shop-2", false)
	code, out, stderr = runCLI(t, "databases", "delete", secondID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("databases delete --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-delete-json", normalizeGolden(out))
}

// TestGoldenDatabasesBackup：备份动词组双形态（F2.2，ADR-0039）。golden
// 夹具引擎无 ObjectStore/Utility 装配——触发后行恒 pending（执行面由
// engine 环测与 dind 演练承担），列表/触发回显形态确定性成立。
func TestGoldenDatabasesBackup(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "dbs-backup")
	dbID, _ := createDatabase(t, projectID, "postgres", "shop", false)

	code, out, stderr := runCLI(t, "databases", "backup", dbID)
	if code != 0 || stderr != "" {
		t.Fatalf("databases backup: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-backup", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "backup", dbID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("databases backup --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-backup-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "backups", dbID)
	if code != 0 || stderr != "" {
		t.Fatalf("databases backups: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-backups", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "backups", dbID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("databases backups --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-backups-json", normalizeGolden(out))

	// verify：golden 夹具无执行链装配，pending 行的拒绝面即确定性输出
	//（E_INVALID_ARGUMENT 信封 stderr；成功面由 e2e 演练承载）。
	code, out, stderr = runCLI(t, "databases", "backup", dbID)
	if code != 0 || stderr != "" {
		t.Fatalf("databases backup round 3: code=%d stderr=%q", code, stderr)
	}
	backupID := extractTailID(out)
	code, _, stderr = runCLI(t, "databases", "verify", backupID)
	if code == 0 {
		t.Fatalf("verifying a pending backup must fail, got exit 0")
	}
	compareGolden(t, "databases-verify", normalizeGolden(stderr))
	code, _, stderr = runCLI(t, "databases", "verify", backupID, "--json")
	if code == 0 {
		t.Fatalf("verifying a pending backup (--json) must fail, got exit 0")
	}
	compareGolden(t, "databases-verify-json", normalizeGolden(stderr))

	// 恢复受理拒绝面（engine 不匹配）：postgres 备份源进 redis 库——
	// 旗标前置（Go flag 首位置参停析，围栏约定），E_INVALID_ARGUMENT 钉死
	//（恢复链路全绿面在 e2e 演练）。
	code, _, stderr = runCLI(t, "databases", "create", "--project", projectID,
		"--engine", "redis", "--restore-from-backup", backupID, "restored")
	if code == 0 {
		t.Fatalf("cross-engine restore must be rejected, got exit 0")
	}
	compareGolden(t, "databases-restore-mismatch", normalizeGolden(stderr))
}

// TestGoldenDatabasesBrowse：browse 动词双形态（F3.6，ADR-0051）。golden
// 夹具引擎注入 BrowseConfig（host_suffix/gateway_url 形态确定性）；postgres
// 库推到 running 后受理。URL/票据/会话 id 均随机——normalize 掩码钉形。
func TestGoldenDatabasesBrowse(t *testing.T) {
	h := apitest.NewManualOpts(t, func(o *engine.Options) {
		o.Browse = engine.BrowseConfig{
			HostSuffix: "browse.test", GatewayURL: "http://127.0.0.1:9081", TLSMode: "none",
		}
	})
	origDial := dialClient
	dialClient = func(_ string, opts ...sdk.Option) (*sdk.Client, error) {
		return sdk.Dial("passthrough:///bufnet", append(opts, h.DialOpts()...)...)
	}
	t.Cleanup(func() { dialClient = origDial })
	t.Setenv("FLEETLY_TOKEN", h.Token)
	projectID := seedDatabaseProject(t, "dbs-browse")
	dbID, _ := createDatabase(t, projectID, "postgres", "shop", false)
	// 库状态推到 running（受理前置：E_DATABASE_NOT_READY 拒 pending）。
	h.Drive(sdk.WithToken(context.Background(), h.Token))
	h.Runtime.ReportRunning(dbID, capability.Generation(1))
	h.Drive(sdk.WithToken(context.Background(), h.Token))

	code, out, stderr := runCLI(t, "databases", "browse", dbID)
	if code != 0 || stderr != "" {
		t.Fatalf("databases browse: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-browse", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "browse", dbID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("databases browse --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-browse-json", normalizeGolden(out))
}
