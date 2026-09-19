package state

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// createTestApp 是 env/placement/volume 测试的应用夹具（返回 ID）。
func createTestApp(t *testing.T, st *Store, name string) string {
	t.Helper()
	app, err := st.CreateApp(context.Background(), "", name)
	if err != nil {
		t.Fatalf("create app %s: %v", name, err)
	}
	return app.ID
}

// TestSetAppEnvPendingLifecycle 验收 3 的存储侧（S16-C4 语义，以引擎现行
// 为准）：SetAppEnv 创建 pending；合并消费面 = ListAppEnv 全量行（引擎
// 传全量——pending 参与合并，部署即消费点）；MarkAppEnvEffective 在部署
// 成功后统一提升；再次 Set 覆盖既有 effective 行回到 pending（生效语义 =
// 下次部署）。
func TestSetAppEnvPendingLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	appID := createTestApp(t, st, "my-api")

	v, err := st.SetAppEnv(ctx, appID, "DATABASE_URL", "cipher-aabbcc", "platform")
	if err != nil {
		t.Fatalf("set env: %v", err)
	}
	if v.Status != EnvStatusPending {
		t.Fatalf("new env status = %s, want pending", v.Status)
	}
	if v.Source != "platform" {
		t.Fatalf("source = %s, want platform", v.Source)
	}

	// 合并消费面：ListAppEnv 返回全量行（含 pending——引擎合并面）。
	rows, err := st.ListAppEnv(ctx, appID)
	if err != nil {
		t.Fatalf("list env: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != EnvStatusPending {
		t.Fatalf("rows before promote = %+v, want 1 pending", rows)
	}

	// 部署成功后的消费点（observing.go succeedDeployment）：全部提升。
	n, err := st.MarkAppEnvEffective(ctx, appID)
	if err != nil {
		t.Fatalf("mark effective: %v", err)
	}
	if n != 1 {
		t.Fatalf("promoted = %d, want 1", n)
	}
	promoted, err := st.GetAppEnv(ctx, appID, "DATABASE_URL")
	if err != nil || promoted.Status != EnvStatusEffective || promoted.Value != "cipher-aabbcc" {
		t.Fatalf("env after promote = %+v err=%v, want effective + value", promoted, err)
	}

	// 覆盖既有 effective 行 → 回到 pending（新值随下次部署生效）。
	if _, err := st.SetAppEnv(ctx, appID, "DATABASE_URL", "cipher-dddddd", ""); err != nil {
		t.Fatalf("overwrite env: %v", err)
	}
	got, err := st.GetAppEnv(ctx, appID, "DATABASE_URL")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if got.Status != EnvStatusPending || got.Value != "cipher-dddddd" {
		t.Fatalf("overwritten env = %+v, want pending + new value", got)
	}
	// 幂等提升。
	if n, err := st.MarkAppEnvEffective(ctx, appID); err != nil || n != 1 {
		t.Fatalf("second promote = %d err=%v", n, err)
	}
}

// TestEnvVarsSourceSystem 留位语义：source=system 合法（模板连接串只读，
// v0.1 无连接串）；非法 source 拒绝。
func TestEnvVarsSourceSystem(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	appID := createTestApp(t, st, "sys-app")

	if _, err := st.SetAppEnv(ctx, appID, "CONN", "cipher-x", "system"); err != nil {
		t.Fatalf("system source: %v", err)
	}
	if _, err := st.SetAppEnv(ctx, appID, "CONN2", "cipher-x", "bogus"); err == nil {
		t.Fatal("bogus source accepted")
	}
}

// TestEnvKeyValidation 键校验：空键/含空白/含 = 拒绝。
func TestEnvKeyValidation(t *testing.T) {
	for _, key := range []string{"", " A", "A ", "A=B", "A\tB", "A\nB"} {
		if err := ValidateEnvKey(key); err == nil {
			t.Errorf("key %q accepted", key)
		}
	}
	for _, key := range []string{"DATABASE_URL", "my-app.var_2", "a-b.c_d1"} {
		if err := ValidateEnvKey(key); err != nil {
			t.Errorf("key %q rejected: %v", key, err)
		}
	}
}

// TestEnvAuditNoValue 负面断言（state-model §2.9）：env 写/删/提升的审计
// 行只含键名，密文值不出现在 audit_log 任何文本列。
func TestEnvAuditNoValue(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	appID := createTestApp(t, st, "audit-app")

	const cipher = "age-encryted-DO-NOT-LEAK-payload"
	if _, err := st.SetAppEnv(ctx, appID, "SECRET_KEY", cipher, "platform"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := st.DeleteAppEnv(ctx, appID, "SECRET_KEY"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := st.DeleteAppEnv(ctx, appID, "SECRET_KEY"); !errors.Is(err, ErrEnvNotFound) {
		t.Fatalf("second delete err = %v, want ErrEnvNotFound", err)
	}

	recs, err := st.RecentAudits(ctx, 100)
	if err != nil {
		t.Fatalf("audits: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("no audit rows written")
	}
	// RecentAudits 投影不含 diff_summary——直接查原文列断言。
	var summaries []string
	rows, err := st.db.QueryContext(ctx, `SELECT diff_summary FROM audit_log`)
	if err != nil {
		t.Fatalf("query audit raw: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		summaries = append(summaries, s)
	}
	for _, s := range summaries {
		if strings.Contains(s, cipher) {
			t.Errorf("audit diff_summary 泄露密文: %s", s)
		}
	}
}

// TestPlacementBindingLifecycle 绑定 CRUD + etag：无条件 upsert、CAS 冲突、
// pinned_at 只在首次置位。
func TestPlacementBindingLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	appID := createTestApp(t, st, "placed-app")

	if _, err := st.GetPlacement(ctx, appID); !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("empty placement err = %v, want ErrPlacementNotFound", err)
	}

	p, err := st.BindPlacement(ctx, PlacementWrite{
		AppID:          appID,
		PlatformNodeID: "n_01",
		Source:         PlacementSourcePlatform,
		Pinned:         true,
	})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if p.State != PlacementBound || p.Etag == "" || p.PinnedAt.IsZero() {
		t.Fatalf("bound placement = %+v", p)
	}

	// label 引导落 label 来源 + label_ref 原值保留。
	p2, err := st.BindPlacement(ctx, PlacementWrite{
		AppID:          appID,
		PlatformNodeID: "n_01",
		Source:         PlacementSourceLabel,
		LabelRef:       "srv-01",
		ExpectedEtag:   p.Etag,
	})
	if err != nil {
		t.Fatalf("rebind with etag: %v", err)
	}
	if p2.Source != PlacementSourceLabel || p2.LabelRef != "srv-01" {
		t.Fatalf("label binding = %+v", p2)
	}
	if p2.PinnedAt.IsZero() {
		t.Fatal("pinned_at washed by rebind (must keep first pin)")
	}

	// CAS：旧 etag 写入 → ErrVersionConflict（E_STATE_VERSION_CONFLICT 语义）。
	_, err = st.BindPlacement(ctx, PlacementWrite{
		AppID: appID, PlatformNodeID: "n_02", ExpectedEtag: p.Etag,
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale etag err = %v, want ErrVersionConflict", err)
	}
	// 词表校验。
	_, err = st.BindPlacement(ctx, PlacementWrite{AppID: appID, PlatformNodeID: "n_01", State: "weird"})
	if err == nil {
		t.Fatal("invalid state accepted")
	}
	_, err = st.BindPlacement(ctx, PlacementWrite{AppID: appID, PlatformNodeID: "n_01", Source: "weird"})
	if err == nil {
		t.Fatal("invalid source accepted")
	}
}

// TestPlacementByAppBatch S18-A4：批量绑定读取——一次 IN 查询返回已有绑定
// 的 app 集；无绑定 app 不在 map（ErrPlacementNotFound 的批量等价形态）；
// 空 ID 集直接空 map。
func TestPlacementByAppBatch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	bound := createTestApp(t, st, "batch-bound")
	blocked := createTestApp(t, st, "batch-blocked")
	unbound := createTestApp(t, st, "batch-unbound")

	if _, err := st.BindPlacement(ctx, PlacementWrite{
		AppID: bound, PlatformNodeID: "n_01", Source: PlacementSourcePlatform,
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := st.BindPlacement(ctx, PlacementWrite{
		AppID: blocked, PlatformNodeID: "n_02", State: PlacementBlocked, Source: PlacementSourcePlatform,
	}); err != nil {
		t.Fatalf("bind blocked: %v", err)
	}

	got, err := st.PlacementByApp(ctx, []string{bound, blocked, unbound})
	if err != nil {
		t.Fatalf("PlacementByApp: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("placements = %d keys, want 2 (unbound absent): %+v", len(got), got)
	}
	if p, ok := got[bound]; !ok || p.State != PlacementBound || p.PlatformNodeID != "n_01" {
		t.Fatalf("bound placement = %+v ok=%v", p, ok)
	}
	if p, ok := got[blocked]; !ok || p.State != PlacementBlocked {
		t.Fatalf("blocked placement = %+v ok=%v", p, ok)
	}

	empty, err := st.PlacementByApp(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty batch = (%d, %v), want (0, nil)", len(empty), err)
	}
}

// TestVolumeRegistry 卷注册表：命名约定名登记、重新声明转 active、孤儿化。
func TestVolumeRegistry(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	appID := createTestApp(t, st, "vol-app")

	v, isNew, err := st.RegisterAppVolume(ctx, VolumeWrite{
		AppID: appID, Key: "data", Name: "fleetly-vol-app-data-deadbeef",
		Kind: VolumeKindNamed, PlatformNodeID: "n_01", MountPath: "/var/lib/db",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !isNew {
		t.Fatal("first registration should report isNew")
	}
	if v.Status != VolumeActive || v.Kind != VolumeKindNamed || v.MountPath != "/var/lib/db" {
		t.Fatalf("registered volume = %+v", v)
	}
	if _, _, err := st.RegisterAppVolume(ctx, VolumeWrite{AppID: appID, Key: "x", Name: "n", Kind: "tmpfs"}); err == nil {
		t.Fatal("invalid kind accepted")
	}

	if n, err := st.MarkAppVolumesOrphaned(ctx, appID); err != nil || n != 1 {
		t.Fatalf("orphan = %d err=%v", n, err)
	}
	got, err := st.GetAppVolume(ctx, appID, "data")
	if err != nil || got.Status != VolumeOrphaned {
		t.Fatalf("orphaned volume = %+v err=%v", got, err)
	}
	// 重新声明 → 复活为 active（isNew=false，created_at 保持）。
	rev, isNew2, err := st.RegisterAppVolume(ctx, VolumeWrite{
		AppID: appID, Key: "data", Name: "fleetly-vol-app-data-deadbeef", PlatformNodeID: "n_01",
	})
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if isNew2 {
		t.Fatal("re-registration should not report isNew")
	}
	if rev.Status != VolumeActive {
		t.Fatalf("re-registered status = %s, want active", rev.Status)
	}
	if !rev.CreatedAt.Equal(v.CreatedAt) {
		t.Fatal("created_at washed on re-register")
	}
	if _, err := st.GetAppVolume(ctx, appID, "nope"); !errors.Is(err, ErrVolumeNotFound) {
		t.Fatalf("missing volume err = %v", err)
	}
}
