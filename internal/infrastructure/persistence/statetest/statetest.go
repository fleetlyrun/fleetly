// Package statetest 是 state 层的 hermetic 测试夹具（架构 §11 自制
// hermetic：真 SQLite + 假底座 + 假时钟）：temp 目录真库 + 可推进假时钟，
// golden 与竞态测试据此获得确定性时间戳。
package statetest

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// FakeClock 是可控墙钟（并发安全；Advance 单调推进）。
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// fixedOrigin 是假时钟固定起点（golden 时间戳确定性锚）。
const fixedOrigin = "2026-01-01T00:00:00Z"

// NewFakeClock 返回固定起点假时钟。
func NewFakeClock() *FakeClock {
	return &FakeClock{now: MustParse(fixedOrigin)}
}

// Now 返回当前假时刻（UTC）。
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance 推进假时钟。
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// MustParse 解析 RFC3339（夹具内固定字面量，失败即测试代码错误）。
func MustParse(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("statetest: bad fixture time " + s + ": " + err.Error())
	}
	return t
}

// New 打开 temp 真库（假时钟注入）并完成迁移；t.Cleanup 自动关闭。
// 同批注入确定性铸造源（ADR-0092）：固定时钟 + 计数熵——repo/apitest/CLI
// golden 全链经此装配，铸造出的 ULID 全程可复现（golden 钉死 id 串的前提；
// 恢复挂 t.Cleanup。SwapForTest 的唯一生产测试入口，守卫执法调用面）。
func New(t testing.TB) (*persistence.DB, *FakeClock) {
	t.Helper()
	clock := NewFakeClock()
	restore := entityid.SwapForTest(&mintEntropy{})
	t.Cleanup(restore)
	path := filepath.Join(t.TempDir(), "fleetly.db")
	db, err := persistence.Open(context.Background(), path, clock)
	if err != nil {
		t.Fatalf("statetest: open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("statetest: close: %v", err)
		}
	})
	// 生产启动恒先种 default Team（authn 首启流）再受理任何 Project；
	// 夹具同口径先落父行——00047 起 projects.team_id 有外键，缺 default
	// Team 的库不是合法生产形态。default Team 固定 seq 0（最早一行），
	// 其余夹具父行用高基数 seq，不占 seq_counters 的小序号空间。
	SeedTeam(t, db, "default")
	return db, clock
}

// mintEntropy 是计数熵：10 字节大端计数器逐铸造递增（朴素 byte 计数 256
// 次回绕撞号）。并发安全由 entityid 铸造锁承载。
type mintEntropy struct{ n uint64 }

func (m *mintEntropy) Read(p []byte) (int, error) {
	m.n++
	for i := 0; i < len(p) && i < 8; i++ {
		p[len(p)-1-i] = byte(m.n >> (8 * i)) //nolint:gosec // G115 误报：测试计数熵的有意截断
	}
	return len(p), nil
}

// ---- 核心资源图父行夹具（00047 外键落地后，子表 repo 测试必须先有父行） ----
//
// 夹具 seq 默认从 100000 起（seedSeqExpr），与 repo 的 NextSeq 小序号空间
// 不相交：父行只为满足外键存在性，不推进 seq_counters，也不改子表测试
// 的创建序断言。INSERT OR IGNORE 让同一父行可被多次引用。

const seedTime = "2026-01-01T00:00:00Z"

// seedSeqExpr 返回父行夹具的 seq 表达式（高基数 + 当前行数，表内不撞号）。
func seedSeqExpr(table string) string {
	return fmt.Sprintf("(SELECT 100000 + COUNT(*) FROM %s)", table)
}

// SeedTeam 落一行 Team 夹具（幂等；name = id）。default Team 固定 seq 0：
// 生产首启种子的 default 恒是最早一行（List seq 序居首，CLI golden 锚），
// 且不与测试自插的 seq 1+ 行撞 UNIQUE(seq)；其余夹具用高基数 seq。
func SeedTeam(t testing.TB, db state.DB, id string) {
	t.Helper()
	seqExpr := seedSeqExpr("teams")
	if id == "default" {
		seqExpr = "0"
	}
	_, err := db.Runner().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO teams (id, seq, name, created_at) SELECT ?, `+seqExpr+`, ?, ?`,
		id, id, seedTime)
	if err != nil {
		t.Fatalf("statetest: seed team %s: %v", id, err)
	}
}

// SeedProject 落一行 Project 夹具（幂等；team 行不存在时一并落）。
func SeedProject(t testing.TB, db state.DB, id, teamID string) {
	t.Helper()
	if teamID == "" {
		teamID = "default"
	}
	SeedTeam(t, db, teamID)
	_, err := db.Runner().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO projects (id, seq, name, team_id, created_at, updated_at, deleted_at)
		 SELECT ?, `+seedSeqExpr("projects")+`, ?, ?, ?, ?, ''`,
		id, id, teamID, seedTime, seedTime)
	if err != nil {
		t.Fatalf("statetest: seed project %s: %v", id, err)
	}
}

// SeedApp 落一行 App 夹具（幂等；project 行不存在时以 default team 一并落）。
func SeedApp(t testing.TB, db state.DB, id, projectID string) {
	t.Helper()
	SeedProject(t, db, projectID, "default")
	_, err := db.Runner().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO apps (id, seq, project_id, name, created_at, updated_at, deleted_at)
		 SELECT ?, `+seedSeqExpr("apps")+`, ?, ?, ?, ?, ''`,
		id, projectID, id, seedTime, seedTime)
	if err != nil {
		t.Fatalf("statetest: seed app %s: %v", id, err)
	}
}

// SeedRevision 落一行 Revision 夹具（幂等；app 行不存在时一并落）。
func SeedRevision(t testing.TB, db state.DB, id, appID string) {
	t.Helper()
	SeedApp(t, db, appID, "default")
	_, err := db.Runner().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO revisions (id, app_id, seq, digest, spec, created_at, source_text)
		 SELECT ?, ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM revisions WHERE app_id = ?), ?, x'7b7d', ?, ''`,
		id, appID, appID, "sha256:"+id, seedTime)
	if err != nil {
		t.Fatalf("statetest: seed revision %s: %v", id, err)
	}
}

// SeedTask 落一行 Task 夹具（幂等；project 行不存在时一并落）。
func SeedTask(t testing.TB, db state.DB, id, projectID string) {
	t.Helper()
	SeedProject(t, db, projectID, "default")
	_, err := db.Runner().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO tasks (id, seq, project_id, name, form, state, spec, created_at, updated_at)
		 SELECT ?, `+seedSeqExpr("tasks")+`, ?, '', 'one-shot', 'active', x'7b7d', ?, ?`,
		id, projectID, seedTime, seedTime)
	if err != nil {
		t.Fatalf("statetest: seed task %s: %v", id, err)
	}
}

// SeedDatabase 落一行 Database 夹具（幂等；project 行不存在时一并落）。
func SeedDatabase(t testing.TB, db state.DB, id, projectID string) {
	t.Helper()
	SeedProject(t, db, projectID, "default")
	_, err := db.Runner().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO databases
		 (id, seq, project_id, name, engine, credentials_ref, version, created_at, updated_at, deleted_at)
		 SELECT ?, `+seedSeqExpr("databases")+`, ?, ?, 'postgres', ?, '18', ?, ?, ''`,
		id, projectID, id, "database:"+id, seedTime, seedTime)
	if err != nil {
		t.Fatalf("statetest: seed database %s: %v", id, err)
	}
}

// SeedPubsub 落一行 PubSub 夹具（幂等；project 行不存在时一并落）。
func SeedPubsub(t testing.TB, db state.DB, id, projectID string) {
	t.Helper()
	SeedProject(t, db, projectID, "default")
	_, err := db.Runner().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO pubsubs
		 (id, seq, project_id, name, engine, version, credentials_ref, created_at, updated_at)
		 SELECT ?, `+seedSeqExpr("pubsubs")+`, ?, ?, 'nats', '2.11', ?, ?, ?`,
		id, projectID, id, "pubsub:"+id, seedTime, seedTime)
	if err != nil {
		t.Fatalf("statetest: seed pubsub %s: %v", id, err)
	}
}

// SeedBucketStore 落一行 BucketStore 夹具（幂等；project 行不存在时一并落）。
func SeedBucketStore(t testing.TB, db state.DB, id, projectID string) {
	t.Helper()
	SeedProject(t, db, projectID, "default")
	_, err := db.Runner().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO bucket_stores
		 (id, seq, project_id, name, engine, version, credentials_ref, created_at, updated_at)
		 SELECT ?, `+seedSeqExpr("bucket_stores")+`, ?, ?, 'silo', '2024', ?, ?, ?`,
		id, projectID, id, "bucketstore:"+id, seedTime, seedTime)
	if err != nil {
		t.Fatalf("statetest: seed bucket store %s: %v", id, err)
	}
}

// SeedNetwork 落一行 Network 夹具（幂等；project 行不存在时一并落）。
func SeedNetwork(t testing.TB, db state.DB, id, projectID string) {
	t.Helper()
	SeedProject(t, db, projectID, "default")
	_, err := db.Runner().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO networks (id, seq, project_id, name, created_at, deleted_at)
		 SELECT ?, `+seedSeqExpr("networks")+`, ?, ?, ?, ''`,
		id, projectID, id, seedTime)
	if err != nil {
		t.Fatalf("statetest: seed network %s: %v", id, err)
	}
}
