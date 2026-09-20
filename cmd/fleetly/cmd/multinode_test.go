package cmd

// 多节点动词测试（E1-7/E1-8，multi-node §2.3/§2.6/§2.8）：join 向导的
// D-MN-13 门禁与向导材料、token 轮换、volumes 清单、rebind 的本地确认门。
// 服务面 = apitest（真实 api 实现 + bufconn），CLI 经生产同路径消费。

import (
	"context"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// startCLIJoin 拨通带 join 向导面的服务面（base_domain 非空 + fake join
// 端口——见 apitest.StartWithJoin）。
func startCLIJoin(t *testing.T) *apitest.Env {
	t.Helper()
	env := apitest.StartWithJoin(t)
	restore := env.DialOptions()
	saved := extraDialOptions
	extraDialOptions = restore
	t.Cleanup(func() { extraDialOptions = saved })
	t.Setenv("FLEETLY_ADDR", "passthrough:///bufnet")
	t.Setenv("FLEETLY_TOKEN", env.AdminToken)
	return env
}

// TestCLIJoinGuideRequiresBaseDomain：base_domain 未配置 → 服务端 409 信封
// （E_MULTI_NODE_REQUIRES_BASE_DOMAIN）经 CLI 渲染，退出码 1（D-MN-13）。
func TestCLIJoinGuideRequiresBaseDomain(t *testing.T) {
	startCLI(t)
	code, _, errOut := runCLIConn(t, "nodes", "join-guide", "--worker-ip", "203.0.113.9")
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if !strings.Contains(errOut, "E_MULTI_NODE_REQUIRES_BASE_DOMAIN") {
		t.Fatalf("stderr must carry the gate code, got: %s", errOut)
	}
}

// TestCLIJoinGuide：向导材料输出（join 命令/规则文本/DNS 步骤/完成判据/
// HA 边界诚实口径尾部）与 --json 机器面。
func TestCLIJoinGuide(t *testing.T) {
	startCLIJoin(t)
	code, out, errOut := runCLIConn(t, "nodes", "join-guide", "--worker-ip", "203.0.113.9")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	for _, want := range []string{
		"docker swarm join --token swmtkn-apitest-worker-token 198.51.100.10:2377",
		"--dport 2377", "203.0.113.9", "ctrl.example.test", "node.joined",
		"HA boundary", "2 nodes",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("join guide output missing %q:\n%s", want, out)
		}
	}

	code, out, _ = runCLIConn(t, "nodes", "join-guide", "--json")
	if code != 0 {
		t.Fatalf("json code=%d", code)
	}
	for _, want := range []string{`"join_command"`, `"worker_token"`, `"manager_rules"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("json output missing %q:\n%s", want, out)
		}
	}
}

// TestCLIRotateToken：轮换输出新 token（文本与 --json）；--role 词表校验
// 在本地拒绝（64）。
func TestCLIRotateToken(t *testing.T) {
	startCLIJoin(t)
	code, out, errOut := runCLIConn(t, "nodes", "rotate-token")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "swmtkn-rotated-worker") || !strings.Contains(out, "worker") {
		t.Fatalf("rotate output = %s", out)
	}
	code, out, _ = runCLIConn(t, "nodes", "rotate-token", "--role", "manager", "--json")
	if code != 0 || !strings.Contains(out, `"role": "manager"`) {
		t.Fatalf("manager rotate code=%d out=%s", code, out)
	}
	code, _, errOut = runCLIConn(t, "nodes", "rotate-token", "--role", "bogus")
	if code != 64 {
		t.Fatalf("bogus role code=%d, want 64; stderr=%s", code, errOut)
	}
}

// TestCLIVolumesList：卷清单文本与 --orphaned/--residual 过滤经真实服务面。
func TestCLIVolumesList(t *testing.T) {
	env := startCLI(t)
	ctx := context.Background()
	app, err := env.Store.CreateApp(ctx, "", "web")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if _, _, err := env.Store.RegisterAppVolume(ctx, state.VolumeWrite{
		AppID: app.ID, Key: "data", Name: "fleetly-web-data-aaaaaaaa",
		PlatformNodeID: "n_dst",
	}); err != nil {
		t.Fatalf("register volume: %v", err)
	}
	// rebind 原语登记 prev（跨节点迁移过的卷 → residual）。
	if err := env.Store.InTx(ctx, func(tx *state.Tx) error {
		_, err := tx.RebindAppVolumes(ctx, app.ID, "n_new")
		return err
	}); err != nil {
		t.Fatalf("rebind volumes: %v", err)
	}

	code, out, errOut := runCLIConn(t, "volumes", "list")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "fleetly-web-data-aaaaaaaa") || !strings.Contains(out, "residual") {
		t.Fatalf("volumes output = %s", out)
	}
	code, out, _ = runCLIConn(t, "volumes", "list", "--residual")
	if code != 0 || !strings.Contains(out, "n_new") {
		t.Fatalf("residual filter code=%d out=%s", code, out)
	}
	code, out, _ = runCLIConn(t, "volumes", "list", "--orphaned")
	if code != 0 || strings.Contains(out, "fleetly-web-data-aaaaaaaa") {
		t.Fatalf("orphaned filter must exclude active rows: %s", out)
	}
	code, out, _ = runCLIConn(t, "volumes", "list", "--json")
	if code != 0 || !strings.Contains(out, `"volumes"`) {
		t.Fatalf("json volumes code=%d out=%s", code, out)
	}
}

// TestCLIPlacementRebindConfirmGate：rebind 缺 --confirm-destructive 在
// 本地拒绝（退出 64，不触服务面）；rebind 面的换点裁决已由 api/placement
// 测试覆盖（apitest 装配为只读形态——写面如实报不可用）。
func TestCLIPlacementRebindConfirmGate(t *testing.T) {
	startCLI(t)
	code, _, errOut := runCLIConn(t, "placement", "rebind", "web", "--node", "srv-02", "--data-restored")
	if code != 64 {
		t.Fatalf("code=%d, want 64; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "--confirm-destructive") {
		t.Fatalf("usage error must name the missing flag: %s", errOut)
	}
	// 互斥处置声明同样本地拒绝。
	code, _, _ = runCLIConn(t, "placement", "rebind", "web", "--node", "srv-02",
		"--data-restored", "--discard", "--confirm-destructive")
	if code != 64 {
		t.Fatalf("mutually exclusive acks code=%d, want 64", code)
	}
}

// TestCLIPlacementMigrateUsage：migrate 缺 --to 在本地拒绝（64）；runbook
// 内容面已由 api 测试覆盖（apitest 为只读装配形态）。
func TestCLIPlacementMigrateUsage(t *testing.T) {
	startCLI(t)
	code, _, errOut := runCLIConn(t, "placement", "migrate", "web")
	if code != 64 {
		t.Fatalf("code=%d, want 64; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "--to") {
		t.Fatalf("usage error must name --to: %s", errOut)
	}
}
