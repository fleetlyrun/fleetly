package cmd

// databases 命令组 golden（F1.12，ADR-0029）：create/list/get/delete 双形
// 态；独立项目夹具（含网络——数据库可达性前置），删除后卷与凭证 Secret
// 残留的口径在 apitest 承载。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
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

	// name 形态定位（ADR-0092）：裸 name + --project 兄弟旗标——输出与
	// id 形态同构（入口不同、body 同源，golden 分开钉）。
	code, out, stderr = runCLI(t, "databases", "get", "--project", "dbs", "shop")
	if code != 0 || stderr != "" {
		t.Fatalf("databases get (by name): code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-get-by-name", normalizeGolden(out))

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
		o.Data.Browse = engine.BrowseConfig{
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
	// 库状态推到 running（受理前置：E_DATABASE_NOT_READY 拒 pending）。观测
	// 经 Watch 消费 goroutine 异步进缓存——固定两拍会与消费时序竞争（S18b
	// browse flake），改用条件等待。
	h.Drive(sdk.WithToken(context.Background(), h.Token))
	h.Runtime.ReportRunning(dbID, capability.Generation(1))
	waitDatabaseRunning(t, h, dbID)

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

// TestGoldenDatabasesCreateFaces（B2c，ADR-0094 决策 3 / ADR-0095 决策 1/7）：
// version/settings/resources 三受理面的创建 golden——显式版本+旋钮+资源
// 双形态、未知版本拒绝、白名单外旋钮拒绝（E_INVALID_ARGUMENT 信封 stderr）。
// Deprecated 两态（无 restore 拒/同版本 restore 放行）在真注册表无弃用条目
// （首批矩阵 EOL 线不入册），由 dbtemplate.AdmitCreateVersion 纯核单测钉死
// （dbtemplate_resolve_test.go；B2a resolveVersionSpec 红灯实验同款先例）。
func TestGoldenDatabasesCreateFaces(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "dbs-knobs")

	// 显式版本 + 旋钮 + 资源：人类/JSON 双形态（回显 = 行值直读）。
	args := []string{"databases", "create", "--project", projectID, "--engine", "postgres",
		"--version", "17", "--set", "max_connections=200", "--cpu-millis", "2000", "--memory-mb", "1024", "tuned"}
	code, out, stderr := runCLI(t, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("databases create (knobs): code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-create-knobs", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "create", "--project", projectID, "--engine", "redis",
		"--version", "7.4", "--set", "appendonly=true", "tuned-json", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("databases create (knobs json): code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-create-knobs-json", normalizeGolden(out))

	// 未知版本：E_INVALID_ARGUMENT 列合法值。
	code, _, stderr = runCLI(t, "databases", "create", "--project", projectID,
		"--engine", "postgres", "--version", "19", "novel")
	if code == 0 {
		t.Fatalf("unknown version must be rejected, got exit 0")
	}
	compareGolden(t, "databases-create-version-unknown", normalizeGolden(stderr))

	// 白名单外旋钮：ValidateSettings 精确报错直通（列合法键）。
	code, _, stderr = runCLI(t, "databases", "create", "--project", projectID,
		"--engine", "postgres", "--set", "listen_addresses=0.0.0.0", "rogue")
	if code == 0 {
		t.Fatalf("unregistered knob must be rejected, got exit 0")
	}
	compareGolden(t, "databases-create-setting-unknown", normalizeGolden(stderr))

	// 资源超限：受理位上限拒绝（64 vCPU / 256 GiB 自定合理界）。
	code, _, stderr = runCLI(t, "databases", "create", "--project", projectID,
		"--engine", "postgres", "--cpu-millis", "999999", "huge")
	if code == 0 {
		t.Fatalf("out-of-range resources must be rejected, got exit 0")
	}
	compareGolden(t, "databases-create-resources-range", normalizeGolden(stderr))
}

// TestGoldenEnginesCatalog（B2c，ADR-0094 决策 4）：Engine Catalog 双形态
// ——服务端注册表现场投影（引擎/版本/默认/弃用/旋钮 schema/能力；digest
// 不出目录——三层解耦的平台层）。
func TestGoldenEnginesCatalog(t *testing.T) {
	_ = newGoldenHarness(t)

	code, out, stderr := runCLI(t, "engines", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("engines list: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "engines-list", normalizeGolden(out))

	code, out, stderr = runCLI(t, "engines", "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("engines list --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "engines-list-json", normalizeGolden(out))
}

// seedRunningDatabase 建 postgres 17 库（带一枚 static 旋钮基线
// max_connections=200——补丁语义「未点名键保留」的可见锚）并推到 running
// （Update 受理门：status != running 拒 E_CONFLICT，红队 A6）。running 观
// 测按该库最近一次 Ensure 的 Generation 回推（apitest driveDatabaseRunning
// 同法），条件等待去时序假设（S18b）。
func seedRunningDatabase(t *testing.T, h *apitest.Harness, projectID, name string) string {
	t.Helper()
	code, out, stderr := runCLI(t, "databases", "create", "--project", projectID, "--engine", "postgres",
		"--version", "17", "--set", "max_connections=200", name)
	require.Zero(t, code, "seed database %s: %s", name, stderr)
	dbID := extractTailID(out)
	require.NotEmpty(t, dbID, "seed database %s: no id in %q", name, out)
	h.Drive(sdk.WithToken(context.Background(), h.Token))
	var gen capability.Generation
	for _, call := range h.Runtime.Calls() {
		if call.NS.Database == dbID {
			gen = call.Gen
		}
	}
	require.NotZero(t, gen, "seed database %s: the carrier must have converged through Ensure", name)
	h.Runtime.ReportRunning(dbID, gen)
	waitDatabaseRunning(t, h, dbID)
	return dbID
}

// previewTicketFromCLI 走两调用流第 1 步（CLI --preview --json）取票——
// 票面经 CLI 自身合并路径铸出，第 2 步带同一组旗标即同一合并期望态摘要
// （补丁语义跨两次调用稳定的端到端证据）。旗标前置于 DATABASE_ID（Go flag
// 首位置参停析；--json 是 root 旗标任意位置剥取）。
func previewTicketFromCLI(t *testing.T, dbID string, flags ...string) string {
	t.Helper()
	args := append(append([]string{"databases", "update"}, flags...), "--preview", dbID, "--json")
	code, out, stderr := runCLI(t, args...)
	require.Zero(t, code, "preview leg: %s", stderr)
	var payload struct {
		Ticket string `json:"ticket"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload), "preview --json must be protojson: %q", out)
	require.NotEmpty(t, payload.Ticket)
	return payload.Ticket
}

// TestGoldenDatabasesUpdate（B3，ADR-0095 决策 5/9 + ADR-0089）：update 动词
// 确认三形态（--preview 退出 / --yes / --ticket 两调用流 / 交互 y·N）+
// 拒绝面（非 TTY 本地指引 / 伪造票 MISMATCH / 非 running E_CONFLICT）+
// 用法面（无变更 / set·unset 冲突 / 确认旗标互斥 / 值类型）。补丁语义
// 可见锚：基线 max_connections=200 在 --set 其他键后保留（回执 settings
// 全集行）；--unset 复位为引擎缺省（预演 diff 的 (default) 端点）。
func TestGoldenDatabasesUpdate(t *testing.T) {
	h := newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "dbs-update")
	base := seedRunningDatabase(t, h, projectID, "tuned")

	// ---- --preview：预演 + ticket 行后退出（零写）。混合变更全景：
	// --unset static 键 + --set dynamic 键 + 资源轴（rollout=replace）。----
	t.Run("preview", func(t *testing.T) {
		dualForm(t, "databases update preview", []string{"databases", "update",
			"--unset", "max_connections", "--set", "work_mem=64mb", "--cpu-millis", "1000", "--preview", base})
	})
	// dynamic-only 预演：zero_touch（容器不重启的直行面）。
	t.Run("preview zero touch", func(t *testing.T) {
		code, out, stderr := runCLI(t, "databases", "update", "--set", "work_mem=32mb", "--preview", base)
		require.Zero(t, code, "zero-touch preview: %s", stderr)
		compareGolden(t, "databases-update-preview-zero-touch", normalizeConfirmGolden(out))
	})

	// ---- --yes：skip 直发 + 一行说明（人类形态）；两车道 + 资源轴同发。
	// 每轮现铸库（执行后同参重放是 no-op，--json 轮须新世界）。----
	t.Run("yes", func(t *testing.T) {
		dualFormRound(t, "databases update", func(label string) []string {
			dbID := seedRunningDatabase(t, h, projectID, "upd-yes-"+label)
			return []string{"databases", "update", "--set", "work_mem=64mb", "--set", "shared_buffers=256mb",
				"--memory-mb", "512", "--yes", dbID}
		})
	})

	// ---- --ticket：两调用流第 2 步（票经 CLI --preview --json 铸出）。----
	t.Run("ticket", func(t *testing.T) {
		dualFormRound(t, "databases update ticket", func(label string) []string {
			dbID := seedRunningDatabase(t, h, projectID, "upd-ticket-"+label)
			ticket := previewTicketFromCLI(t, dbID, "--set", "work_mem=64mb")
			return []string{"databases", "update", "--set", "work_mem=64mb", "--ticket", ticket, dbID}
		})
	})

	// ---- 交互形态（伪 TTY）：预演打印 + y/N 提示 + y 带票直发 / N 中止。----
	t.Run("interactive confirm", func(t *testing.T) {
		fakeConfirmTTY(t, "y\n")
		dualFormRound(t, "databases update interactive", func(label string) []string {
			dbID := seedRunningDatabase(t, h, projectID, "upd-tty-"+label)
			return []string{"databases", "update", "--set", "shared_buffers=512mb", dbID}
		})
	})
	t.Run("interactive abort", func(t *testing.T) {
		fakeConfirmTTY(t, "n\n")
		code, out, stderr := runCLI(t, "databases", "update", "--set", "shared_buffers=512mb", base)
		require.Zero(t, code, "interactive abort: %s", stderr)
		compareGolden(t, "databases-update-interactive-abort", normalizeConfirmGolden(out))
	})

	// ---- 拒绝面：非 TTY 无旗标（本地指引，不发请求）/ 伪造票 / 非 running。----
	t.Run("non-tty refusal", func(t *testing.T) {
		forceNoTTY(t)
		dualFormErr(t, "databases update confirm-required",
			[]string{"databases", "update", "--set", "work_mem=64mb", base}, 1)
	})
	t.Run("ticket mismatch", func(t *testing.T) {
		dualFormErr(t, "databases update ticket-mismatch",
			[]string{"databases", "update", "--set", "work_mem=64mb", "--ticket", "bogus-ticket", base}, 1)
	})
	t.Run("not running", func(t *testing.T) {
		code, out, stderr := runCLI(t, "databases", "create", "--project", projectID, "--engine", "redis", "fresh")
		require.Zero(t, code, "seed pending database: %s", stderr)
		pending := extractTailID(out)
		code, _, stderr = runCLI(t, "databases", "update", "--set", "maxmemory=256mb", "--yes", pending)
		require.Equal(t, 1, code, "a pending database must refuse updates (E_CONFLICT)")
		compareGolden(t, "databases-update-not-running", normalizeGolden(stderr))
	})

	// ---- 用法面（exit 64，拨号前）。----
	t.Run("usage", func(t *testing.T) {
		for _, args := range [][]string{
			{"databases", "update", base}, // 无变更
			{"databases", "update", "--set", "work_mem=1mb", "--unset", "work_mem", base}, // set/unset 冲突
			{"databases", "update", "--set", "work_mem", base},                            // 非 KEY=VALUE
			{"databases", "update", "--set", "work_mem=1mb", "--yes", "--preview", base},  // 确认旗标互斥
			{"databases", "update", "--cpu-millis", "lots", base},                         // 值类型
			{"databases", "update", "--cpu-millis", "-1", base},                           // 界外
			{"databases", "update", "--set", "work_mem=1mb", base, "extra"},               // 多余位置参数
		} {
			code, _, _ := runCLI(t, args...)
			require.Equal(t, exitUsage, code, "%v must be a usage error", args)
		}
	})
}

// TestGoldenDatabasesConfigDescribe（B3，ADR-0095 决策 10 / design §3.5）：
// 目录驱动旋钮面——组列举双形态 + describe 双形态（缺省版本）+ 显式版本
// 形态（redis：bool/enum/无缺省列）+ 零旋钮引擎 + 引擎/版本未在册拒绝。
func TestGoldenDatabasesConfigDescribe(t *testing.T) {
	_ = newGoldenHarness(t)

	for _, tc := range []struct {
		golden string
		args   []string
	}{
		{"databases-config", []string{"databases", "config"}},
		{"databases-config-json", []string{"databases", "config", "--json"}},
		{"databases-config-describe", []string{"databases", "config", "describe", "postgres"}},
		{"databases-config-describe-json", []string{"databases", "config", "describe", "postgres", "--json"}},
		{"databases-config-describe-version", []string{"databases", "config", "describe", "--version", "7.4", "redis"}},
		{"databases-config-describe-no-knobs", []string{"databases", "config", "describe", "mongo"}},
	} {
		code, out, stderr := runCLI(t, tc.args...)
		require.Zero(t, code, "%v: %s", tc.args, stderr)
		compareGolden(t, tc.golden, normalizeGolden(out))
	}

	for _, tc := range []struct {
		golden string
		args   []string
	}{
		{"databases-config-describe-unknown-engine", []string{"databases", "config", "describe", "nosuch"}},
		{"databases-config-describe-unknown-version", []string{"databases", "config", "describe", "--version", "99", "postgres"}},
	} {
		code, out, stderr := runCLI(t, tc.args...)
		require.Equal(t, 1, code, "%v must fail (catalog lookup)", tc.args)
		require.Empty(t, out)
		compareGolden(t, tc.golden, normalizeGolden(stderr))
	}

	code, _, _ := runCLI(t, "databases", "config", "describe")
	require.Equal(t, exitUsage, code, "describe without ENGINE is a usage error")
}
