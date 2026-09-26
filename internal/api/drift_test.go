package api

// Drift 三端点的引用寻址回归：resolveApp 过门禁后必须把**解析后的三段限
// 定形**传引擎（engine 三方法按 GetAppByName 直查，不识别引用形态）——原始
// 引用（Console 主路径的路由参数 = 平台 id）透传使 id 寻址恒 404；裸业务
// 名在跨项目同名时撞 state.ErrAppAmbiguous 裸抛 500（2026-09-26 staging
// demo 双项目重名实录）。覆盖 id（26 字符 ULID）与 team/prj/app 限定形两
// 种引用 × GET/PUT/POST，及跨项目重名形态的存续回归。

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// driftSubstrateStub 是引擎底座端口的最小桩：inspect 恒缺失（DriftShow 走
// missing 漂移投影，不触真实 swarm）；create/update 恒成功（ConvergeApp 的
// 归位重放对空实况集全量 ServiceCreate）。仅本文件进程内夹具使用。
type driftSubstrateStub struct{}

func (driftSubstrateStub) SwarmReady(context.Context) error { return nil }

func (driftSubstrateStub) NetworkEnsure(context.Context, string) error { return nil }

func (driftSubstrateStub) ServiceInspect(context.Context, string) (engine.ServiceState, error) {
	return engine.ServiceState{}, engine.ErrServiceNotFound
}

func (driftSubstrateStub) ServiceCreate(context.Context, engine.ServiceSpec) error { return nil }

func (driftSubstrateStub) ServiceUpdate(context.Context, string, engine.ServiceSpec) error {
	return nil
}

func (driftSubstrateStub) ServiceRemove(context.Context, string) error { return nil }

func (driftSubstrateStub) ServiceList(context.Context, map[string]string) ([]engine.ServiceState, error) {
	return nil, nil
}

func (driftSubstrateStub) TaskList(context.Context, string) ([]engine.TaskState, error) {
	return nil, nil
}

// newDriftTestEnv 起一个只挂 DriftService 的 bufconn server（harness 风格：
// 真实 store + 生产同构鉴权链 + admin scope token）；引擎底座为
// driftSubstrateStub。种子应用带一条 succeeded 部署行（密文期望快照）——
// DriftShow 有基线可比、ConvergeApp 有收敛目标；返回该行的 desired_hash
// 供收敛回执断言。
func newDriftTestEnv(t *testing.T) (*testEnv, *grpc.ClientConn, state.App, string) {
	t.Helper()
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

	env := &testEnv{st: st, box: box, fixtureProject: seedFixtureProject(t, st)}
	env.admTok = env.seedToken(t, "admin")

	app, err := st.CreateApp(context.Background(), "", "driftapp", env.fixtureProject.ID, env.fixtureProject.TeamID)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// succeeded 部署行 + 密文期望快照（与引擎 succeedDeployment 同形态；
	// 快照 label 带三段限定形 app——applyDesired 的归属识别面要求）。
	spec := engine.ServiceSpec{
		Name:     "fleetly-tfixture-fixture-driftapp-web",
		Image:    "nginx:1.27",
		Replicas: 1,
		ServiceLabels: map[string]string{
			state.LabelManaged: state.ManagedLabelValue,
			state.LabelApp:     app.QualifiedName(),
		},
	}
	raw, err := json.Marshal([]engine.ServiceSpec{spec})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	ct, err := box.Encrypt(raw)
	if err != nil {
		t.Fatalf("encrypt snapshot: %v", err)
	}
	rec, err := st.CreateDeployment(context.Background(), state.DeployRecord{
		AppID: app.ID, AppName: app.Name, Kind: "deploy", DesiredSpec: string(ct),
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if err := st.InTx(context.Background(), func(tx *state.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`UPDATE deployments SET status = 'succeeded', desired_hash = ? WHERE id = ?`,
			spec.DesiredHash(), rec.ID)
		return err
	}); err != nil {
		t.Fatalf("seed succeeded row: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	eng := engine.NewEngine(engine.Config{}, st, driftSubstrateStub{}, nil, nil, box, logger)
	srv := newAuthServer(NewAuthenticator(st))
	serverv1.RegisterDriftServiceServer(srv, NewDriftService(st, eng))
	conn := serveBufconn(t, srv)
	return env, conn, app, spec.DesiredHash()
}

func TestDriftEndpointsResolveAppReferenceToName(t *testing.T) {
	env, conn, app, baselineHash := newDriftTestEnv(t)
	ctx := context.Background()
	drift := serverv1.NewDriftServiceClient(conn)

	refs := map[string]string{
		"id":        app.ID,
		"qualified": "tfixture/fixture/driftapp",
	}
	for label, ref := range refs {
		// GET ShowDrift：id/限定形寻址到达引擎（有基线 + 服务缺失 → drifted
		// 报告，200），报告 App 回业务名——修复前 id 形态 404
		//（app not found: <ULID>）。
		show, err := drift.ShowDrift(authCtx(ctx, env.admTok), &serverv1.ShowDriftRequest{App: ref})
		if err != nil {
			t.Fatalf("[%s] ShowDrift(%q): %v", label, ref, err)
		}
		if show.GetApp() != "driftapp" {
			t.Fatalf("[%s] ShowDrift report app = %q, want driftapp", label, show.GetApp())
		}
		if !show.GetDrifted() || show.GetDesiredDeployment() == "" {
			t.Fatalf("[%s] ShowDrift report = drifted=%v baseline=%q, want drifted with baseline", label, show.GetDrifted(), show.GetDesiredDeployment())
		}

		// PUT SetDriftConverge：置位落该 app 行（引擎按名更新），200 回显。
		set, err := drift.SetDriftConverge(authCtx(ctx, env.admTok), &serverv1.SetDriftConvergeRequest{App: ref, Enabled: true})
		if err != nil {
			t.Fatalf("[%s] SetDriftConverge(%q): %v", label, ref, err)
		}
		if !set.GetEnabled() {
			t.Fatalf("[%s] SetDriftConverge echo = %+v, want enabled", label, set)
		}
		on, err := env.st.GetAppDriftConverge(ctx, app.ID)
		if err != nil || !on {
			t.Fatalf("[%s] opt-in not persisted on the seeded app row: on=%v err=%v", label, on, err)
		}

		// POST ConvergeDrift：归位重放入队成功（200），回收敛基准部署 =
		// 种子的 succeeded 行——修复前 id 形态在引擎侧 404。
		conv, err := drift.ConvergeDrift(authCtx(ctx, env.admTok), &serverv1.ConvergeDriftRequest{App: ref})
		if err != nil {
			t.Fatalf("[%s] ConvergeDrift(%q): %v", label, ref, err)
		}
		if conv.GetDeploymentId() == "" || conv.GetDesiredHash() != baselineHash {
			t.Fatalf("[%s] ConvergeDrift receipt = %+v, want seeded baseline deployment", label, conv)
		}
	}

	// 对照（业务名直查不受扰）：修复语义只改引用→名，不添副作用。
	show, err := drift.ShowDrift(authCtx(ctx, env.admTok), &serverv1.ShowDriftRequest{App: "driftapp"})
	if err != nil {
		t.Fatalf("bare-name ShowDrift control: %v", err)
	}
	if show.GetApp() != "driftapp" {
		t.Fatalf("bare-name ShowDrift control app = %q", show.GetApp())
	}
}

// TestDriftEndpointsSurviveCrossProjectDuplicateName 跨项目同名 app（设计
// 能力：「prod 与 dev 各有 demo」）下的 id 寻址漂移面（2026-09-26 staging
// 500 回归，demo 双项目重名实录）：引擎按名重解析撞 state.ErrAppAmbiguous
// 裸抛（gateway degraded envelope，grpc Unknown → 500）的形态修复后——
// 引擎收到解析后的三段限定形（GetAppByQualifiedName 精确命中），id/限定
// 形寻址三端点全 200 且落行/收敛目标都指向 id 寻址的那一行。
func TestDriftEndpointsSurviveCrossProjectDuplicateName(t *testing.T) {
	env, conn, app, baselineHash := newDriftTestEnv(t)
	ctx := context.Background()
	// 第二团队/项目下播种同名应用——制造裸名歧义面（生产正路创建）。
	dup := seedDuplicateNameApp(t, env.st, app.Name)

	drift := serverv1.NewDriftServiceClient(conn)
	refs := map[string]string{
		"id":        app.ID,
		"qualified": app.QualifiedName(),
	}
	for label, ref := range refs {
		// GET ShowDrift：200 漂移报告（有基线 + 服务缺失 → drifted），报告
		// App 回业务名——修复前引擎收到裸名「driftapp」恒撞歧义裸抛 500。
		show, err := drift.ShowDrift(authCtx(ctx, env.admTok), &serverv1.ShowDriftRequest{App: ref})
		if err != nil {
			t.Fatalf("[%s] ShowDrift(%q): %v", label, ref, err)
		}
		if show.GetApp() != "driftapp" || !show.GetDrifted() || show.GetDesiredDeployment() == "" {
			t.Fatalf("[%s] ShowDrift report = app=%q drifted=%v baseline=%q, want drifted with baseline", label, show.GetApp(), show.GetDrifted(), show.GetDesiredDeployment())
		}

		// PUT SetDriftConverge：opt-in 落在 id 寻址的 app 行（不是重名兄弟行）。
		if _, err := drift.SetDriftConverge(authCtx(ctx, env.admTok), &serverv1.SetDriftConvergeRequest{App: ref, Enabled: true}); err != nil {
			t.Fatalf("[%s] SetDriftConverge(%q): %v", label, ref, err)
		}
		on, err := env.st.GetAppDriftConverge(ctx, app.ID)
		if err != nil || !on {
			t.Fatalf("[%s] opt-in not persisted on the addressed row: on=%v err=%v", label, on, err)
		}
		off, err := env.st.GetAppDriftConverge(ctx, dup.ID)
		if err != nil && !errors.Is(err, state.ErrAppNotFound) {
			t.Fatalf("[%s] duplicate sibling opt-in read: %v", label, err)
		}
		if off {
			t.Fatalf("[%s] duplicate sibling row touched: opt-in enabled", label)
		}

		// POST ConvergeDrift：归位重放入队成功，收敛基准 = 寻址行的种子部署。
		conv, err := drift.ConvergeDrift(authCtx(ctx, env.admTok), &serverv1.ConvergeDriftRequest{App: ref})
		if err != nil {
			t.Fatalf("[%s] ConvergeDrift(%q): %v", label, ref, err)
		}
		if conv.GetDeploymentId() == "" || conv.GetDesiredHash() != baselineHash {
			t.Fatalf("[%s] ConvergeDrift receipt = %+v, want seeded baseline deployment", label, conv)
		}
	}

	// 裸名对照：跨项目重名下 api 解析面显性拒绝（E_APP_AMBIGUOUS 信封文案
	// 带可行动指引——区别于引擎裸错误「app name is ambiguous across
	// projects」的歧义原文）。
	if _, err := drift.ShowDrift(authCtx(ctx, env.admTok), &serverv1.ShowDriftRequest{App: "driftapp"}); err == nil {
		t.Fatal("bare-name ShowDrift accepted under cross-project duplicate (want E_APP_AMBIGUOUS)")
	} else if msg := status.Convert(err).Message(); !strings.Contains(msg, "resolves to multiple rows across projects") {
		t.Fatalf("bare-name ShowDrift error message = %q, want the structured ambiguity envelope", msg)
	}
}

// seedDuplicateNameApp 在独立第二团队/项目下播种同名应用（跨项目重名夹具；
// 生产正路 CreateTeam/CreateProject/CreateApp）。
func seedDuplicateNameApp(t *testing.T, st *state.Store, name string) state.App {
	t.Helper()
	team, err := st.CreateTeam(context.Background(), state.TeamWrite{
		Slug: "tsecond", Name: "second team", CreatedBy: "fixture",
	})
	if err != nil {
		t.Fatalf("create second team: %v", err)
	}
	proj, err := st.CreateProject(context.Background(), state.ProjectWrite{
		TeamID: team.ID, Slug: "second", Name: "second project",
	})
	if err != nil {
		t.Fatalf("create second project: %v", err)
	}
	app, err := st.CreateApp(context.Background(), "", name, proj.ID, team.ID)
	if err != nil {
		t.Fatalf("create duplicate-name app: %v", err)
	}
	return app
}
