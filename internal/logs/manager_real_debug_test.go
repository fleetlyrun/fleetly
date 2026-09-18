//go:build fleetly_docker

package logs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/substrate"
)

// TestRealManagerDebug 逐段定位采集循环（实机诊断用；与
// TestRealManagerCollect 同环境）。
func TestRealManagerDebug(t *testing.T) {
	app := os.Getenv("FLEETLY_LOG_APP")
	if app == "" {
		app = "t27web"
	}
	db := filepath.Join(os.TempDir(), "fleetly-rt", "fleetly.db")
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

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	apps, err := st.ListActiveApps(ctx)
	t.Logf("ListActiveApps: n=%d err=%v", len(apps), err)
	var target *state.App
	for i := range apps {
		if apps[i].Name == app {
			target = &apps[i]
		}
	}
	if target == nil {
		t.Fatalf("app %s not active", app)
	}

	svcs, err := sc.ManagedServiceProcesses(ctx, target.Name)
	t.Logf("ManagedServiceProcesses(%s): %v err=%v", target.Name, svcs, err)
	if err != nil || len(svcs) == 0 {
		t.Fatal("discovery failed")
	}

	swarmName, err := naming.ServiceName(target.Name, svcs[0])
	t.Logf("swarm name: %s err=%v", swarmName, err)

	since := time.Now().Add(-time.Hour)
	lines, err := sc.StreamServiceLogs(ctx, swarmName, since, false)
	t.Logf("StreamServiceLogs(since=%s): err=%v", since.Format(time.RFC3339Nano), err)
	if err != nil {
		t.Fatal("stream open failed")
	}
	n := 0
	for l := range lines {
		n++
		if n <= 2 {
			t.Logf("line: at=%s content=%q", l.At, l.Line)
		}
		if n >= 5 {
			break
		}
	}
	t.Logf("collected %d lines (bounded break)", n)

	// now 起点形态（首轮采集游标 = clock()）。
	lines2, err := sc.StreamServiceLogs(ctx, swarmName, time.Now(), false)
	t.Logf("StreamServiceLogs(since=now): err=%v", err)
	if err == nil {
		n2 := 0
		for range lines2 {
			n2++
			if n2 >= 3 {
				break
			}
		}
		t.Logf("from-now lines (3s window): %d", n2)
	}
}
