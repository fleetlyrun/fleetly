package api

// ListHistoryLogs 的引用寻址回归（2026-09-26 staging 500 修复，多应用复现
// ——Console followLogs 页的初始回填走本面）：Console 主路径以路由参数（平
// 台 id）寻址，原始引用透传使 logs.Manager.History 内部 GetAppByName 恒
// miss（裸 app not found 未映射错误码 → grpc Unknown → 裸 500）。修复后
// handler 下发解析后的三段限定形——id/限定形寻址均 200，行归属按寻址 app
// 精确收口；跨项目同名形态（staging demo 双项目重名）不落歧义面。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/logs"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

func TestListHistoryLogsResolveAppReference(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(filepath.Join(dir, "test.key"))
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	fixture := seedFixtureProject(t, st)

	app, err := st.CreateApp(context.Background(), "", "webapp", fixture.ID, fixture.TeamID)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	// 第二团队/项目下同名 app——staging demo 同款跨项目重名形态（裸名歧义
	// 面；Manager 内部 GetAppByName 兼容限定形后不落歧义）。
	dup := seedDuplicateNameApp(t, st, app.Name)

	// build 来源历史行（免底座：builds 表 log_path 产物——Manager.History
	// 的 build 分支只读表与文件）。
	logPath := filepath.Join(dir, "build.log")
	if err := os.WriteFile(logPath, []byte("step 1/3 resolve\nstep 2/3 build\nstep 3/3 export\n"), 0o600); err != nil {
		t.Fatalf("write build log: %v", err)
	}
	if _, err := st.CreateBuild(context.Background(), state.BuildRecord{
		AppID:   app.ID,
		Service: "web",
		Driver:  state.DriverRailpack,
		Status:  state.BuildQueued,
		LogPath: logPath,
	}); err != nil {
		t.Fatalf("create build: %v", err)
	}

	mg := logs.NewManager(logs.Config{Dir: filepath.Join(dir, "logs")}, st, nil, box, nil)
	srv := newAuthServer(NewAuthenticator(st))
	serverv1.RegisterLogsServiceServer(srv, NewLogsService(st, mg))
	conn := serveBufconn(t, srv)
	tok := seedTokenPlain(t, st, "admin")

	cl := serverv1.NewLogsServiceClient(conn)
	ctx := context.Background()
	refs := map[string]string{
		"id":        app.ID,
		"qualified": app.QualifiedName(),
	}
	for label, ref := range refs {
		resp, err := cl.ListHistoryLogs(authCtx(ctx, tok), &serverv1.ListHistoryLogsRequest{
			App: ref, Source: logs.SourceBuild, Limit: 200,
		})
		if err != nil {
			// 修复前 id 形态：Manager 内部 GetAppByName(<ULID>) → 裸
			// app not found（未映射错误码 → 裸 500）。
			t.Fatalf("[%s] ListHistoryLogs(%q): %v", label, ref, err)
		}
		if got := len(resp.GetEntries()); got != 3 {
			t.Fatalf("[%s] entries = %d, want 3", label, got)
		}
		if got := resp.GetEntries()[1].GetLine(); got != "step 2/3 build" {
			t.Fatalf("[%s] entry line = %q, want the middle build step", label, got)
		}
		if got := resp.GetEntries()[0].GetApp(); got != app.QualifiedName() {
			t.Fatalf("[%s] entry app = %q, want qualified form %q", label, got, app.QualifiedName())
		}
	}

	// 重名兄弟行寻址：200 空集（历史行按 app 精确归属，不串流）。
	resp, err := cl.ListHistoryLogs(authCtx(ctx, tok), &serverv1.ListHistoryLogsRequest{
		App: dup.ID, Source: logs.SourceBuild, Limit: 200,
	})
	if err != nil {
		t.Fatalf("duplicate sibling ListHistoryLogs: %v", err)
	}
	if got := len(resp.GetEntries()); got != 0 {
		t.Fatalf("duplicate sibling entries = %d, want 0 (history is addressed per app row)", got)
	}
}
