package api

// CronService RPC 测试（E5 Cron）：TriggerCronRun 的错误映射（无 schedule →
// 404）、触发回执投影、调度器未装配的显式拒绝、台账读面投影与 scope 登记。

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/cron"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// fakeCronTriggers 是调度端口的假件（回放预设行/错误）。
type fakeCronTriggers struct {
	run state.CronRun
	err error
}

func (f fakeCronTriggers) TriggerRun(context.Context, string, string, string, string) (state.CronRun, error) {
	return f.run, f.err
}

// newCronTestServer 起一只只挂 CronService 的 bufconn gRPC（handler 级映射
// 测试；鉴权链在 auth_test 全矩阵覆盖，scope 登记另有结构断言）。
func newCronTestServer(t *testing.T, svc *CronService) serverv1.CronServiceClient {
	t.Helper()
	srv := grpc.NewServer()
	serverv1.RegisterCronServiceServer(srv, svc)
	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return serverv1.NewCronServiceClient(conn)
}

// TestTriggerCronRunErrorMapping：目标服务不构成 cron schedule → NotFound
// （404 语义，文案可行动）；实现方其余错误原样透传。
func TestTriggerCronRunErrorMapping(t *testing.T) {
	env := newTestEnv(t)
	seedApp(t, env.st, "nope")
	cl := newCronTestServer(t, NewCronService(env.st, fakeCronTriggers{err: cron.ErrNoSchedule}))
	_, err := cl.TriggerCronRun(context.Background(), &serverv1.TriggerCronRunRequest{App: "nope", Service: "task"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
	if !strings.Contains(err.Error(), "no cron schedule") {
		t.Fatalf("error text lacks the actionable hint: %v", err)
	}
}

func seedApp(t *testing.T, st *state.Store, name string) state.App {
	t.Helper()
	app, err := st.CreateApp(context.Background(), "", name)
	if err != nil {
		t.Fatalf("create app %s: %v", name, err)
	}
	return app
}

// TestTriggerCronRunPassesRunRow 触发回执投影：started 行原样带出。
func TestTriggerCronRunPassesRunRow(t *testing.T) {
	env := newTestEnv(t)
	seedApp(t, env.st, "app")
	cl := newCronTestServer(t, NewCronService(env.st, fakeCronTriggers{
		run: state.CronRun{ID: "r1", Service: "task", Expression: "* * * * *", Status: state.CronRunStarted, JobService: "fleetly-cron-app-task-abc"},
	}))
	resp, err := cl.TriggerCronRun(context.Background(), &serverv1.TriggerCronRunRequest{App: "app", Service: "task"})
	if err != nil {
		t.Fatalf("TriggerCronRun: %v", err)
	}
	if resp.GetRun().GetId() != "r1" || resp.GetRun().GetStatus() != state.CronRunStarted {
		t.Fatalf("run projection = %+v", resp.GetRun())
	}
}

// TestCronSchedulerNotConfigured 调度器未装配 → Internal（进程内夹具形态的
// 显式不可用，非静默空响应）。
func TestCronSchedulerNotConfigured(t *testing.T) {
	env := newTestEnv(t)
	seedApp(t, env.st, "app")
	cl := newCronTestServer(t, NewCronService(env.st, nil))
	_, err := cl.TriggerCronRun(context.Background(), &serverv1.TriggerCronRunRequest{App: "app", Service: "task"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}

// TestTriggerCronRunMissingApp 目标 app 不存在 → NotFound（resolveApp 统一
// 语义；调度端口不得被触达）。
func TestTriggerCronRunMissingApp(t *testing.T) {
	env := newTestEnv(t)
	cl := newCronTestServer(t, NewCronService(env.st, fakeCronTriggers{err: errors.New("must not be called")}))
	_, err := cl.TriggerCronRun(context.Background(), &serverv1.TriggerCronRunRequest{App: "ghost", Service: "task"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

// TestListCronRunsProjection 台账读面：行投影（含 skipped 原因）+ app 名
// 回带；service 过滤参数直通 state 层（state 侧测试钉死）。
func TestListCronRunsProjection(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	app, err := env.st.CreateApp(ctx, "", "cronapp")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if err := env.st.InTx(ctx, func(tx *state.Tx) error {
		_, err := tx.CreateCronRun(ctx, state.CronRun{
			AppID:       app.ID,
			Service:     "task",
			Expression:  "*/5 * * * *",
			Status:      state.CronRunSkipped,
			SkipReason:  state.CronSkipOverlap,
			ScheduledAt: time.Now().UTC(),
		})
		return err
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	cl := newCronTestServer(t, NewCronService(env.st, nil))
	resp, err := cl.ListCronRuns(ctx, &serverv1.ListCronRunsRequest{App: "cronapp"})
	if err != nil {
		t.Fatalf("ListCronRuns: %v", err)
	}
	if resp.GetApp() != "cronapp" || len(resp.GetRuns()) != 1 {
		t.Fatalf("response = %+v", resp)
	}
	if resp.GetRuns()[0].GetStatus() != state.CronRunSkipped || resp.GetRuns()[0].GetSkipReason() != state.CronSkipOverlap {
		t.Fatalf("row = %+v", resp.GetRuns()[0])
	}
}

// TestCronScopesRegistered scope 矩阵登记（触发=deploy、读面=read；拦截器
// 按 admin fail-closed 的兜底路径不覆盖本面）。
func TestCronScopesRegistered(t *testing.T) {
	if scope, ok := RequiredScope("/fleetly.server.v1.CronService/TriggerCronRun"); !ok || scope != ScopeDeploy {
		t.Fatalf("TriggerCronRun scope = %q ok=%v, want deploy", scope, ok)
	}
	if scope, ok := RequiredScope("/fleetly.server.v1.CronService/ListCronRuns"); !ok || scope != ScopeRead {
		t.Fatalf("ListCronRuns scope = %q ok=%v, want read", scope, ok)
	}
}
