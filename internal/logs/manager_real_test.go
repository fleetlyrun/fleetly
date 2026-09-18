//go:build fleetly_docker

package logs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/substrate"
)

// 实机集成测试（T2-7 验收第 4 项）：Manager 采集循环对真实 Docker 的
// 端到端——扫描 active app 的受管服务 → 轮询容器日志 → 落盘。
//
// 跑法（本机 Docker 可用、且已用 fleetly 部署探针应用时）：
//
//	go test -tags fleetly_docker ./internal/logs/ -run TestRealManagerCollect -v
//
// 环境变量：FLEETLY_LOG_APP（缺省 t27web）、FLEETLY_LOG_DB（目标状态库；
// 缺省 %TEMP%\fleetly-rt\fleetly.db）。

func TestRealManagerCollect(t *testing.T) {
	app := os.Getenv("FLEETLY_LOG_APP")
	if app == "" {
		app = "t27web"
	}
	db := os.Getenv("FLEETLY_LOG_DB")
	if db == "" {
		db = filepath.Join(os.TempDir(), "fleetly-rt", "fleetly.db")
	}
	if _, err := os.Stat(db); err != nil {
		t.Skipf("state db %s not found (start fleetlyd + deploy first)", db)
	}
	st, err := state.Open(context.Background(), db)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	sc, err := substrate.NewClient("")
	if err != nil {
		t.Fatalf("substrate.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })

	dir := filepath.Join(t.TempDir(), "logs")
	mg := NewManager(Config{Dir: dir, ScanIntervalMillis: 500, RingSize: 100}, st, sc, nil, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() { _ = mg.Run(ctx) }()

	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := mg.History(ctx, HistoryQuery{App: app, Source: SourceContainer, Limit: 5})
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(rows) > 0 {
			t.Logf("collected %d lines, first=%q at=%s", len(rows), rows[0].Line, rows[0].At)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	// 诊断：逐段探查。
	apps, _ := st.ListActiveApps(ctx)
	found := false
	for _, a := range apps {
		if a.Name == app {
			found = true
		}
	}
	t.Fatalf("no log entries collected within 7s (apps=%d, app %q found=%v, dir exists=%v)",
		len(apps), app, found, dirExists(dir))
}

func dirExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
