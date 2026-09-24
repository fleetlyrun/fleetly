package state

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

// openWithMigrationsThrough 在 path 上只应用 ≤through 版本的迁移（迁移前
// 现场构造：同一 goose provider 路径 + 过滤后的迁移 FS）。返回的 *sql.DB
// 由调用方关闭。
func openWithMigrationsThrough(ctx context.Context, path string, through int64) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	embedded, err := migrationFiles()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	entries, err := fs.ReadDir(embedded, ".")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	limited := fstest.MapFS{}
	for _, e := range entries {
		lead, _, found := strings.Cut(e.Name(), "_")
		if !found {
			continue
		}
		v, err := strconv.ParseInt(lead, 10, 64)
		if err != nil || v > through {
			continue
		}
		data, err := fs.ReadFile(embedded, e.Name())
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		limited[e.Name()] = &fstest.MapFile{Data: data}
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, limited)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := provider.Up(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// 卷归属泛化的 Go 面验收（managed-databases 设计 §5.4 迁移①，E4 S1）：
// owner 二元组核心 API + app 形态包装零行为变化 + 迁移前后快照
//（存量 app 卷行零语义变化）。

// TestOwnerVolumeRegistration 泛化键（owner_kind, owner_id, key）的登记/
// 查询/孤儿/换绑：app 卷与 database 卷同名 key 不撞（跨归属空间独立）。
func TestOwnerVolumeRegistration(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	appID := createTestApp(t, st, "vol-app")
	dbID := createTestDatabase(t, st, "vol-db")

	// app 归属（既有形态包装）。
	av, isNew, err := st.RegisterAppVolume(ctx, VolumeWrite{
		AppID: appID, Key: "data", Name: "fleetly-vol-app-data-01JABCDE",
		PlatformNodeID: "n_01", MountPath: "/data",
	})
	if err != nil || !isNew {
		t.Fatalf("register app volume = %+v isNew=%v err=%v", av, isNew, err)
	}
	if av.OwnerKind != VolumeOwnerApp || av.OwnerID != appID {
		t.Fatalf("app volume owner = %s/%s, want app/%s", av.OwnerKind, av.OwnerID, appID)
	}

	// database 归属：同名 key 与 app 卷不冲突（唯一键含 owner 二元组）。
	dv, isNew, err := st.RegisterVolume(ctx, VolumeWrite{
		OwnerKind: VolumeOwnerDatabase, OwnerID: dbID, Key: "data", Name: "fleetly-db-vol-db-data-01JABCDE",
		PlatformNodeID: "n_01", MountPath: "/var/lib/postgresql/data",
	})
	if err != nil || !isNew {
		t.Fatalf("register db volume = %+v isNew=%v err=%v", dv, isNew, err)
	}
	if dv.OwnerKind != VolumeOwnerDatabase || dv.OwnerID != dbID {
		t.Fatalf("db volume owner = %s/%s, want database/%s", dv.OwnerKind, dv.OwnerID, dbID)
	}

	// 泛化键取单卷 + 按归属列表互不串。
	got, err := st.GetVolume(ctx, VolumeOwnerDatabase, dbID, "data")
	if err != nil || got.Name != "fleetly-db-vol-db-data-01JABCDE" {
		t.Fatalf("get db volume = %+v err=%v", got, err)
	}
	appVols, err := st.ListOwnerVolumes(ctx, VolumeOwnerApp, appID)
	if err != nil || len(appVols) != 1 {
		t.Fatalf("list app volumes = %+v err=%v, want 1", appVols, err)
	}
	dbVols, err := st.ListOwnerVolumes(ctx, VolumeOwnerDatabase, dbID)
	if err != nil || len(dbVols) != 1 {
		t.Fatalf("list db volumes = %+v err=%v, want 1", dbVols, err)
	}
	all, err := st.ListAllVolumes(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("list all = %+v err=%v, want 2", all, err)
	}

	// 词表外 owner_kind 拒绝；缺 owner 拒绝。
	if _, _, err := st.RegisterVolume(ctx, VolumeWrite{OwnerKind: "bogus", OwnerID: "x", Key: "k", Name: "n"}); err == nil {
		t.Fatal("bogus owner_kind accepted")
	}
	if _, _, err := st.RegisterVolume(ctx, VolumeWrite{OwnerKind: VolumeOwnerDatabase, Key: "k", Name: "n"}); err == nil {
		t.Fatal("missing owner_id accepted")
	}

	// app 卷孤儿（泛化核心 + 包装两条路径等价）。
	n, err := st.MarkOwnerVolumesOrphaned(ctx, VolumeOwnerDatabase, dbID)
	if err != nil || n != 1 {
		t.Fatalf("orphan db volumes = %d err=%v, want 1", n, err)
	}
	orphaned, err := st.GetVolume(ctx, VolumeOwnerDatabase, dbID, "data")
	if err != nil || orphaned.Status != VolumeOrphaned {
		t.Fatalf("db volume after orphan = %+v err=%v", orphaned, err)
	}
	// 重新声明复活（数据诞生点语义跨归属同款）。
	revived, isNew, err := st.RegisterVolume(ctx, VolumeWrite{
		OwnerKind: VolumeOwnerDatabase, OwnerID: dbID, Key: "data", Name: "fleetly-db-vol-db-data-01JABCDE",
		PlatformNodeID: "n_01", MountPath: "/var/lib/postgresql/data",
	})
	if err != nil || isNew || revived.Status != VolumeActive {
		t.Fatalf("revive db volume = %+v isNew=%v err=%v, want active/not-new", revived, isNew, err)
	}

	// 事务内换绑（泛化形态）：database 卷 prev 登记 + 节点迁移。
	err = st.InTx(ctx, func(tx *Tx) error {
		_, err := tx.RebindVolumes(ctx, VolumeOwnerDatabase, dbID, "n_02")
		return err
	})
	if err != nil {
		t.Fatalf("rebind db volumes: %v", err)
	}
	rebound, err := st.GetVolume(ctx, VolumeOwnerDatabase, dbID, "data")
	if err != nil || rebound.PlatformNodeID != "n_02" || rebound.PrevPlatformNodeID != "n_01" {
		t.Fatalf("rebound volume = %+v err=%v, want n_02 with prev n_01", rebound, err)
	}

	// GetVolume 未命中：ErrVolumeNotFound。
	if _, err := st.GetVolume(ctx, VolumeOwnerDatabase, dbID, "missing"); !errors.Is(err, ErrVolumeNotFound) {
		t.Fatalf("missing volume err = %v, want ErrVolumeNotFound", err)
	}
}

// TestVolumesOwnerGeneralizationMigration 迁移快照（验收 4）：00001→00013
// 现场播种 app 卷行 → 应用 00014-00015 → 行语义逐字段保持（owner_kind='app'
// + owner_id=app_id 平移，事实列原值），且旧 schema 的 app_id 列不复存在。
func TestVolumesOwnerGeneralizationMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "migrate.db")

	// ① 只应用 00001-00013（迁移前现场）——用过滤后的迁移 FS 走同一
	// goose provider 路径。
	db, err := openWithMigrationsThrough(ctx, path, 13)
	if err != nil {
		t.Fatalf("apply migrations through 13: %v", err)
	}
	seedApp := "01JABCDEFVOL"
	seedTime := int64(1700000000000000000)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO apps (id, name, lifecycle, created_at, updated_at) VALUES (?, ?, 'active', ?, ?)`,
		seedApp, "seed-app", seedTime, seedTime); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	seedVolumes := []struct {
		id, key, name, node, status, kind, mount, host, prev string
	}{
		{"01JVOL0000000000000000001", "data", "fleetly-seed-app-data-01JABCDEF", "n_01", "active", "named", "/var/lib/data", "", ""},
		{"01JVOL0000000000000000002", "cache", "fleetly-seed-app-cache-01JABCDEF", "n_01", "orphaned", "named", "/cache", "", "n_00"},
	}
	for _, v := range seedVolumes {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO volumes (id, app_id, key, name, platform_node_id, status, created_at, updated_at, kind, mount_path, host_path, prev_platform_node_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			v.id, seedApp, v.key, v.name, v.node, v.status, seedTime, seedTime, v.kind, v.mount, v.host, v.prev); err != nil {
			t.Fatalf("seed volume %s: %v", v.key, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close pre-migration db: %v", err)
	}

	// ② 应用 00014-00015（openWithMigrationsThrough 增量；**不**跑全链——
	// v0.3 为 fresh-install 版本（rbac-teams §8 D-W0-5），00019 起归属列
	// NOT NULL，存量 NULL 归属行的升级路径按设计不存在；本测试钉的是
	// 00014→00015 的卷行语义平移，截链到 15 即可）。
	db2, err := openWithMigrationsThrough(ctx, path, 15)
	if err != nil {
		t.Fatalf("apply migrations through 15: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	st := &Store{db: db2, path: path}

	// ③ app 卷行语义保持：包装读取面逐字段相等（ListAppVolumes 按 key
	// 字典序 → [cache, data]）。
	vols, err := st.ListAppVolumes(ctx, seedApp)
	if err != nil {
		t.Fatalf("list volumes after migration: %v", err)
	}
	if len(vols) != 2 {
		t.Fatalf("volumes after migration = %d, want 2 (rows preserved)", len(vols))
	}
	wantByKey := map[string]struct {
		name, node, status, kind, mount, host, prev string
	}{
		"data":  {name: "fleetly-seed-app-data-01JABCDEF", node: "n_01", status: "active", kind: "named", mount: "/var/lib/data", host: "", prev: ""},
		"cache": {name: "fleetly-seed-app-cache-01JABCDEF", node: "n_01", status: "orphaned", kind: "named", mount: "/cache", host: "", prev: "n_00"},
	}
	for _, v := range vols {
		want := wantByKey[v.Key]
		if v.OwnerKind != VolumeOwnerApp || v.OwnerID != seedApp {
			t.Fatalf("row %s owner = %s/%s, want app/%s (migration must copy owner_kind='app', owner_id=app_id)", v.Key, v.OwnerKind, v.OwnerID, seedApp)
		}
		if v.Name != want.name || string(v.Kind) != want.kind || v.PlatformNodeID != want.node ||
			v.MountPath != want.mount || v.HostPath != want.host || string(v.Status) != want.status ||
			v.PrevPlatformNodeID != want.prev {
			t.Fatalf("row %s facts drifted:\n got %+v\nwant %+v", v.Key, v, want)
		}
		if v.CreatedAt.UnixNano() != seedTime || v.UpdatedAt.UnixNano() != seedTime {
			t.Fatalf("row %s timestamps drifted: %v/%v, want preserved %d", v.Key, v.CreatedAt, v.UpdatedAt, seedTime)
		}
	}

	// ④ 旧列不复存在（表已重建）：查 app_id 列必须失败。
	if _, err := st.db.QueryContext(ctx, `SELECT app_id FROM volumes LIMIT 1`); err == nil {
		t.Fatal("legacy app_id column still present after rebuild migration")
	}

	// ⑤（原「泛化写入通道在迁移后的库上可用」）已随 v0.3 W2-S3 移除：通道
	// 的正路测试在 TestOwnerVolumeRegistration（全链迁移库）；截链到 00015
	// 的库上 teams/projects 表尚不存在（00018 才建），库实例归属写入无从
	// 构造——且 v0.3 fresh-install 前提（D-W0-5）下不存在跨版本混合库。
}
