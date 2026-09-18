// Package apitest 是 CLI/SDK 侧集成测试的进程内服务装配夹具：真实
// internal/api 服务实现 + 独立临时目录 sqlite + 生产同构拦截链（auth）的
// bufconn gRPC server。它存在的理由（T2.18）：cmd/fleetly 测试需要「真实
// api 面」做 golden 快照与退出码断言，但 fence 禁止 cmd/fleetly 触及
// internal/state 等装配依赖——装配知识集中在本包，测试侧只拿连接与
// token。
//
// 注意：本包是测试支持设施，不是产品代码；随服务面扩展同步登记注册。
package apitest

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/api"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/logs"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/substrate"
)

// Env 是一次测试环境：连接、admin token 与底层 store（夹具播种用——仅
// 测试代码经它写行，被测 CLI 只走 gRPC 面）。
type Env struct {
	Conn       *grpc.ClientConn
	AdminToken string
	Store      *state.Store
	Dir        string
	lis        *bufconn.Listener
}

// DialOptions 返回指向进程内 server 的拨号选项（CLI 测试经 extraDialOptions
// 注入——被测面与生产同路径走 fleetly.NewClient）。
func (e *Env) DialOptions() []grpc.DialOption {
	lis := e.lis
	return []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
}

// fakeLogPort 是日志管线的确定性假底座端口：每个服务流首次轮询回放一行
// 固定日志（时间 = 注入时钟），随后永久阻塞——ring/落盘恰好一行，golden
// 可确定快照。
type fakeLogPort struct {
	line string
	at   time.Time
}

func (f *fakeLogPort) StreamServiceLogs(_ context.Context, _ string, _ time.Time, _ bool) (<-chan substrate.LogLine, error) {
	ch := make(chan substrate.LogLine)
	go func() {
		ch <- substrate.LogLine{At: f.at, Line: f.line}
		// 不 close、不再发送：模拟长驻日志流的拉取语义（range 阻塞）。
		block := make(chan struct{})
		<-block
	}()
	return ch, nil
}

func (f *fakeLogPort) ManagedServiceProcesses(_ context.Context, _ string) ([]string, error) {
	return []string{"web"}, nil
}

// Start 起一个完整服务面（除 ingress.Manager——nil 端口形态，入口面如实
// 报告不可用）并返回连接与 admin token；生命周期挂 t.Cleanup。
func Start(t *testing.T) *Env {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("apitest: state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(filepath.Join(dir, "test.key"))
	if err != nil {
		t.Fatalf("apitest: EnsureKey: %v", err)
	}
	token := seedToken(t, st)

	// 日志管线：快扫描 + 确定性单行（fakeLogPort）。
	mgr := logs.NewManager(
		logs.Config{Dir: filepath.Join(dir, "logs"), ScanIntervalMillis: 20}.Normalize(),
		st, &fakeLogPort{line: "hello from web", at: time.Now().UTC().Truncate(time.Second).Add(time.Second)},
		box, slog.New(slog.NewTextHandler(os.Stderr, nil)),
	)
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Run(runCtx) }()

	// 引擎：nil 底座——drift 面的 show（无成功部署）/converge（无目标）/
	// enable/disable 路径不触底座；触底座路径属引擎集成测试职责。
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	eng := engine.NewEngine(engine.Config{}, st, nil, nil, nil, box, logger)

	auth := api.NewAuthenticator(st)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(auth.UnaryAuthInterceptor()),
		grpc.ChainStreamInterceptor(auth.StreamAuthInterceptor()),
	)
	serverv1.RegisterSystemServiceServer(srv, api.NewSystemService("dev", st,
		func() []api.SystemComponent { return nil }, nil))
	serverv1.RegisterAppsServiceServer(srv, api.NewAppsService(st))
	serverv1.RegisterDeploymentsServiceServer(srv, api.NewDeploymentsService(st))
	serverv1.RegisterRevisionsServiceServer(srv, api.NewRevisionsService(st))
	serverv1.RegisterBuildsServiceServer(srv, api.NewBuildsService(st))
	serverv1.RegisterDriftServiceServer(srv, api.NewDriftService(st, eng))
	serverv1.RegisterDomainsServiceServer(srv, api.NewDomainsService(st, nil))
	serverv1.RegisterEnvServiceServer(srv, api.NewEnvService(st, box))
	serverv1.RegisterLogsServiceServer(srv, api.NewLogsService(st, mgr))
	serverv1.RegisterEventsServiceServer(srv, api.NewEventsService(st))
	serverv1.RegisterPlacementServiceServer(srv, api.NewPlacementService(st))
	serverv1.RegisterTokensServiceServer(srv, api.NewTokensService(st))

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
		t.Fatalf("apitest: grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &Env{Conn: conn, AdminToken: token, Store: st, Dir: dir, lis: lis}
}

// seedToken 落一枚 admin token（CLI 鉴权面凭据），返回明文。
func seedToken(t *testing.T, st *state.Store) string {
	t.Helper()
	plaintext, err := api.GenerateBootstrapAdminToken(context.Background(), st, "apitest admin")
	if err != nil {
		t.Fatalf("apitest: seed token: %v", err)
	}
	return plaintext
}

// CreateApp 播种一个应用行（读面测试的既有应用夹具；幂等——已存在时
// 返回既有行）。
func (e *Env) CreateApp(t *testing.T, name string) state.App {
	t.Helper()
	if app, err := e.Store.GetAppByName(context.Background(), name); err == nil {
		return app
	}
	app, err := e.Store.CreateApp(context.Background(), "", name)
	if err != nil {
		t.Fatalf("apitest: create app %s: %v", name, err)
	}
	return app
}

// SeedRevisionFromYAML 经 compose.Load 解析夹具并固化一条成功版本快照
// （ComposeNormalized = canonical JSON——与引擎 succeedDeployment 同形态），
// 返回 revision ID。
func (e *Env) SeedRevisionFromYAML(t *testing.T, appName, yamlText string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o600); err != nil {
		t.Fatalf("apitest: write compose fixture: %v", err)
	}
	spec, _, err := compose.Load(context.Background(), path)
	if err != nil {
		t.Fatalf("apitest: load compose fixture: %v", err)
	}
	app := e.CreateApp(t, appName)
	raw, err := spec.CanonicalJSON()
	if err != nil {
		t.Fatalf("apitest: canonical json: %v", err)
	}
	var rev state.Revision
	err = e.Store.InTx(context.Background(), func(tx *state.Tx) error {
		r, err := tx.CreateRevision(context.Background(), state.RevisionWrite{
			AppID: app.ID, ComposeNormalized: string(raw),
			DesiredHash: spec.SpecHash,
		})
		rev = r
		return err
	})
	if err != nil {
		t.Fatalf("apitest: create revision: %v", err)
	}
	return rev.ID
}

// SeedPlacement 播种已绑定放置 + 已登记命名卷（placement show 夹具）。
// 应用以固定 ID 播种——卷名 `fleetly-<app>-<key>-<appid8>` 含 app ID 前 8
// 位，固定 ID 使 golden 快照确定。
func (e *Env) SeedPlacement(t *testing.T, appName string) string {
	t.Helper()
	app, err := e.Store.GetAppByName(context.Background(), appName)
	if err != nil {
		app, err = e.Store.CreateApp(context.Background(), "01HJKMNP", appName)
		if err != nil {
			t.Fatalf("apitest: create fixed-id app: %v", err)
		}
	}
	platformID := "n_" + "01TESTNODE"
	if _, err := e.Store.BindPlacement(context.Background(), state.PlacementWrite{
		AppID: app.ID, PlatformNodeID: platformID,
		Source: state.PlacementSourcePlatform, Pinned: true,
	}); err != nil {
		t.Fatalf("apitest: bind placement: %v", err)
	}
	volName, err := naming.VolumeName(appName, "data", app.ID)
	if err != nil {
		t.Fatalf("apitest: volume name: %v", err)
	}
	if _, _, err := e.Store.RegisterAppVolume(context.Background(), state.VolumeWrite{
		AppID: app.ID, Key: "data", Name: volName,
		PlatformNodeID: platformID, MountPath: "/var/lib/data",
	}); err != nil {
		t.Fatalf("apitest: register volume: %v", err)
	}
	return platformID
}

// SeedNode 播种一条节点观测缓存行（nodes ls 夹具）。
func (e *Env) SeedNode(t *testing.T) {
	t.Helper()
	err := e.Store.SyncNodeObservations(context.Background(), []state.SubstrateNode{{
		SwarmNodeID: "swarm-test", Hostname: "srv-01", State: "ready", Availability: "active",
		IsManager: true, Labels: map[string]string{state.LabelNodeID: "n_test"},
	}}, time.Now())
	if err != nil {
		t.Fatalf("apitest: seed node: %v", err)
	}
}

// SeedDomain 播种一条域名台账行（domains list 夹具）。
func (e *Env) SeedDomain(t *testing.T, appName, service, domain, port string) {
	t.Helper()
	app := e.CreateApp(t, appName)
	err := e.Store.ReplaceAppDomains(context.Background(), app.ID, []state.DomainServiceRoutes{{
		Service: service, Port: port, Domains: []string{domain},
	}})
	if err != nil {
		t.Fatalf("apitest: seed domain: %v", err)
	}
}

// SeedBuild 播种一条 queued 构建行（builds list 夹具）。
func (e *Env) SeedBuild(t *testing.T, appName, service string) string {
	t.Helper()
	app := e.CreateApp(t, appName)
	rec, err := e.Store.CreateBuild(context.Background(), state.BuildRecord{
		AppID: app.ID, Service: service, Driver: state.DriverRailpack, Request: "{}",
	})
	if err != nil {
		t.Fatalf("apitest: seed build: %v", err)
	}
	return rec.ID
}

// AppendEvent 播种一条注册表内事件（events watch 夹具）。
func (e *Env) AppendEvent(t *testing.T, name, subject, payload string) int64 {
	t.Helper()
	var seq int64
	err := e.Store.InTx(context.Background(), func(tx *state.Tx) error {
		s, err := tx.AppendEvent(context.Background(), state.Event{Name: name, Subject: subject, Payload: payload})
		seq = s
		return err
	})
	if err != nil {
		t.Fatalf("apitest: append event: %v", err)
	}
	return seq
}
