package state

// 自动扩缩策略与运行期副本覆盖层的 state 面测试（W5-S1）：CRUD 往返、
// 校验矩阵（设计 §1.1 约束逐条：min≥1 / max≤16 / max≥min / target∈[20,90]
// / cooldown∈[60,3600] 缺省 180 / 至少一维目标 / 服务名词表）、app 存在性
// 与 active 生命周期门、审计 scaling.policy_changed 同事务落账、覆盖层
// upsert/删除联动。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestScalingPolicyCRUDRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app := seedAppForScaling(t, st, "demo")

	got, err := st.SetScalingPolicy(ctx, app.ID, ScalingPolicy{
		Service:      "web",
		MinReplicas:  1,
		MaxReplicas:  6,
		TargetCPUPct: 60,
		TargetMemPct: 70,
	}, ScalingPolicyOptions{Actor: "human"})
	if err != nil {
		t.Fatalf("set scaling policy: %v", err)
	}
	if got.CooldownSeconds != ScalingDefaultCooldownSeconds {
		t.Fatalf("cooldown default = %d, want %d", got.CooldownSeconds, ScalingDefaultCooldownSeconds)
	}

	row, err := st.GetScalingPolicy(ctx, app.ID, "web")
	if err != nil {
		t.Fatalf("get scaling policy: %v", err)
	}
	if row.MinReplicas != 1 || row.MaxReplicas != 6 || row.TargetCPUPct != 60 || row.TargetMemPct != 70 {
		t.Fatalf("policy row = %+v", row)
	}
	if row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not populated: %+v", row)
	}

	// 整行替换 upsert（同键覆盖）。
	if _, err := st.SetScalingPolicy(ctx, app.ID, ScalingPolicy{
		Service:      "web",
		MinReplicas:  2,
		MaxReplicas:  8,
		TargetCPUPct: 50,
	}, ScalingPolicyOptions{Actor: "human"}); err != nil {
		t.Fatalf("re-set scaling policy: %v", err)
	}
	rows, err := st.ListScalingPolicies(ctx)
	if err != nil {
		t.Fatalf("list scaling policies: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("policy count = %d, want 1 (replace not insert)", len(rows))
	}
	if rows[0].MinReplicas != 2 || rows[0].MaxReplicas != 8 || rows[0].TargetCPUPct != 50 || rows[0].TargetMemPct != 0 {
		t.Fatalf("replaced policy = %+v", rows[0])
	}

	// 删除 + 404 语义。
	if err := st.RemoveScalingPolicy(ctx, app.ID, "web", ScalingPolicyOptions{Actor: "human"}); err != nil {
		t.Fatalf("remove scaling policy: %v", err)
	}
	if _, err := st.GetScalingPolicy(ctx, app.ID, "web"); !errors.Is(err, ErrScalingPolicyNotFound) {
		t.Fatalf("get after remove = %v, want ErrScalingPolicyNotFound", err)
	}
	if err := st.RemoveScalingPolicy(ctx, app.ID, "web", ScalingPolicyOptions{Actor: "human"}); !errors.Is(err, ErrScalingPolicyNotFound) {
		t.Fatalf("remove again = %v, want ErrScalingPolicyNotFound", err)
	}
}

func TestScalingPolicyValidationMatrix(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(p *ScalingPolicy)
		wantErr string
	}{
		{"min below floor", func(p *ScalingPolicy) { p.MinReplicas = 0 }, "below floor"},
		{"max above ceiling", func(p *ScalingPolicy) { p.MaxReplicas = 17 }, "above ceiling"},
		{"max below min", func(p *ScalingPolicy) { p.MinReplicas = 4; p.MaxReplicas = 3 }, "below min_replicas"},
		{"cpu target below range", func(p *ScalingPolicy) { p.TargetCPUPct = 19 }, "outside [20,90]"},
		{"cpu target above range", func(p *ScalingPolicy) { p.TargetCPUPct = 91 }, "outside [20,90]"},
		{"mem target below range", func(p *ScalingPolicy) { p.TargetMemPct = 19 }, "outside [20,90]"},
		{"mem target above range", func(p *ScalingPolicy) { p.TargetMemPct = 91 }, "outside [20,90]"},
		{"no target at all", func(p *ScalingPolicy) { p.TargetCPUPct = 0; p.TargetMemPct = 0 }, "at least one target"},
		{"cooldown below range", func(p *ScalingPolicy) { p.CooldownSeconds = 59 }, "outside [60,3600]"},
		{"cooldown above range", func(p *ScalingPolicy) { p.CooldownSeconds = 3601 }, "outside [60,3600]"},
		{"service name illegal", func(p *ScalingPolicy) { p.Service = "web/x" }, "outside [A-Za-z0-9._-]"},
		{"service name empty", func(p *ScalingPolicy) { p.Service = "" }, "outside [A-Za-z0-9._-]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ScalingPolicy{Service: "web", MinReplicas: 1, MaxReplicas: 4, TargetCPUPct: 60, CooldownSeconds: 180}
			tc.mutate(&p)
			err := ValidateScalingPolicy(&p)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateScalingPolicy(%+v) = %v, want containing %q", p, err, tc.wantErr)
			}
		})
	}

	// 边界值全部合法：min=max=16、target 20/90、cooldown 60/3600、
	// 0 目标维度（单维策略）。
	for _, p := range []ScalingPolicy{
		{Service: "web", MinReplicas: 16, MaxReplicas: 16, TargetCPUPct: 20, TargetMemPct: 90, CooldownSeconds: 60},
		{Service: "web", MinReplicas: 1, MaxReplicas: 16, TargetCPUPct: 90, CooldownSeconds: 3600},
		{Service: "web", MinReplicas: 1, MaxReplicas: 2, TargetMemPct: 20, CooldownSeconds: 0}, // cooldown 缺省
	} {
		if err := ValidateScalingPolicy(&p); err != nil {
			t.Fatalf("ValidateScalingPolicy(%+v) = %v, want nil", p, err)
		}
	}
	def := ScalingPolicy{Service: "web", MinReplicas: 1, MaxReplicas: 2, TargetMemPct: 20, CooldownSeconds: 0}
	if err := ValidateScalingPolicy(&def); err != nil || def.CooldownSeconds != ScalingDefaultCooldownSeconds {
		t.Fatalf("cooldown default not normalized in place (err=%v): %+v", err, def)
	}
}

func TestScalingPolicyAppGuards(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// app 不存在：显式拒绝。
	if _, err := st.SetScalingPolicy(ctx, "no-such-app", ScalingPolicy{
		Service: "web", MinReplicas: 1, MaxReplicas: 2, TargetCPUPct: 60,
	}, ScalingPolicyOptions{Actor: "human"}); err == nil || !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("set on missing app = %v, want ErrAppNotFound", err)
	}

	// deleting app：拒绝写策略。
	app := seedAppForScaling(t, st, "demo")
	if err := st.MarkAppDeleting(ctx, app.ID); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}
	if _, err := st.SetScalingPolicy(ctx, app.ID, ScalingPolicy{
		Service: "web", MinReplicas: 1, MaxReplicas: 2, TargetCPUPct: 60,
	}, ScalingPolicyOptions{Actor: "human"}); err == nil || !strings.Contains(err.Error(), "only active apps") {
		t.Fatalf("set on deleting app = %v, want active-only guard", err)
	}
}

func TestScalingPolicyAuditWritten(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app := seedAppForScaling(t, st, "demo")

	if _, err := st.SetScalingPolicy(ctx, app.ID, ScalingPolicy{
		Service: "web", MinReplicas: 1, MaxReplicas: 4, TargetCPUPct: 60,
	}, ScalingPolicyOptions{Actor: "human"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := st.RemoveScalingPolicy(ctx, app.ID, "web", ScalingPolicyOptions{Actor: "human"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	entries, _, err := st.ListAudits(ctx, AuditQuery{Action: "scaling.policy_changed", Limit: 10})
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("audit entries = %d, want 2 (set + remove)", len(entries))
	}
	for _, e := range entries {
		if e.Actor != "human" || e.Result != "ok" {
			t.Fatalf("audit entry = %+v", e)
		}
	}
	if !strings.Contains(entries[0].DiffSummary, "service") {
		t.Fatalf("diff summary missing service: %q", entries[0].DiffSummary)
	}
}

func TestScalingReplicaOverrideLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app := seedAppForScaling(t, st, "demo")
	// 覆盖层与策略同键域（compose 服务名）——策略撤销联动清场按同键命中。
	svcName := "web"

	if _, err := st.GetScalingReplicaOverride(ctx, app.ID, svcName); !errors.Is(err, ErrScalingOverrideNotFound) {
		t.Fatalf("get missing override = %v, want ErrScalingOverrideNotFound", err)
	}
	if err := st.UpsertScalingReplicaOverride(ctx, ScalingReplicaOverride{
		AppID: app.ID, Service: svcName, DeploymentID: "dep-1", Replicas: 3,
	}); err != nil {
		t.Fatalf("upsert override: %v", err)
	}
	ov, err := st.GetScalingReplicaOverride(ctx, app.ID, svcName)
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if ov.DeploymentID != "dep-1" || ov.Replicas != 3 {
		t.Fatalf("override = %+v", ov)
	}

	// 覆盖行更新（同键换部署/换副本）。
	if err := st.UpsertScalingReplicaOverride(ctx, ScalingReplicaOverride{
		AppID: app.ID, Service: svcName, DeploymentID: "dep-2", Replicas: 5,
	}); err != nil {
		t.Fatalf("re-upsert override: %v", err)
	}
	ov, err = st.GetScalingReplicaOverride(ctx, app.ID, svcName)
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if ov.DeploymentID != "dep-2" || ov.Replicas != 5 {
		t.Fatalf("override after update = %+v", ov)
	}
	list, err := st.ListScalingReplicaOverrides(ctx, app.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list overrides = %v (%v), want 1 row", list, err)
	}

	// RemoveScalingPolicy 联动清覆盖行（策略撤销 → 期望回落 compose 快照）。
	if _, err := st.SetScalingPolicy(ctx, app.ID, ScalingPolicy{
		Service: "web", MinReplicas: 1, MaxReplicas: 8, TargetCPUPct: 60,
	}, ScalingPolicyOptions{Actor: "human"}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if err := st.RemoveScalingPolicy(ctx, app.ID, "web", ScalingPolicyOptions{Actor: "human"}); err != nil {
		t.Fatalf("remove policy: %v", err)
	}
	if _, err := st.GetScalingReplicaOverride(ctx, app.ID, svcName); !errors.Is(err, ErrScalingOverrideNotFound) {
		t.Fatalf("override after policy removal = %v, want ErrScalingOverrideNotFound", err)
	}
}

// seedAppForScaling 播种一个 active app（testfixture 的包内助手同构：
// 个人队 + default 项目）。
func seedAppForScaling(t *testing.T, st *Store, name string) App {
	t.Helper()
	app, err := seedAppE(t, st, name)
	if err != nil {
		t.Fatalf("seed app %s: %v", name, err)
	}
	return app
}
