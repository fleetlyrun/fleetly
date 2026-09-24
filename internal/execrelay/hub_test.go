package execrelay

// 控制面 hub 单测：连接表（注册/顶替/摘除）、会话目标选择、并发限额
//（per-token 2 / 全局上限）、ticket 一次性消费路径、审计+事件双落（含
// 「会话内容零泄漏」负向断言——明文纪律的验收面）、集群 token 校验。

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// fakeTasks 是 TaskSource 假实现（服务名 → 任务集；nodeID → 节点 hostname）。
type fakeTasks struct {
	tasks   map[string][]TaskRuntime
	hosts   map[string]string
	hostErr error
}

func (f *fakeTasks) ListTaskRuntimes(_ context.Context, service string) ([]TaskRuntime, error) {
	if f.tasks == nil {
		return nil, nil
	}
	return f.tasks[service], nil
}

func (f *fakeTasks) NodeHostnames(_ context.Context) (map[string]string, error) {
	if f.hostErr != nil {
		return nil, f.hostErr
	}
	return f.hosts, nil
}

type hubFixture struct {
	hub   *Hub
	st    *state.Store
	tasks *fakeTasks
	conn  *fakeConn
	rc    *relayConn
}

// newHubFixture 装配 hub + 假底座（fleetly-exec 服务任务 hostname=abc123…
// → 节点 n1；demo 应用 web 服务一个 running 任务在 n1）。
func newHubFixture(t *testing.T, mutate func(cfg *HubConfig)) *hubFixture {
	t.Helper()
	st, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ft := &fakeTasks{
		tasks: map[string][]TaskRuntime{
			ExecRelayServiceName: {
				{ID: "rtask1", NodeID: "n1", ContainerID: "abc123def4567890", State: "running", DesiredState: "running", Timestamp: time.Now()},
			},
			"fleetly-acme-prod-demo-web": {
				{ID: "task-9", NodeID: "n1", ContainerID: "cafe1234567890", State: "running", DesiredState: "running", Timestamp: time.Now()},
				{ID: "task-old", NodeID: "n1", ContainerID: "old1234567890", State: "shutdown", DesiredState: "shutdown", Timestamp: time.Now()},
			},
		},
		// swarm 实态：任务容器缺省 hostname = 节点 hostname（mgr）。
		hosts: map[string]string{"n1": "mgr"},
	}
	conn := newFakeConn()
	hub := NewHub(HubConfig{
		Store:   st,
		Tasks:   ft,
		Tickets: NewTicketStore(0),
	})
	if mutate != nil {
		mutate(&hub.cfg)
	}
	return &hubFixture{hub: hub, st: st, tasks: ft, conn: conn}
}

// registerDefault 以 fixture 连接注册 relay（成员发现正路径）。
func (f *hubFixture) registerDefault(t *testing.T) {
	t.Helper()
	rc, err := f.hub.RegisterRelay(context.Background(), f.conn, "abc123def456")
	if err != nil {
		t.Fatalf("RegisterRelay: %v", err)
	}
	f.rc = rc
}

// eventsOf 读全部事件。
func (f *hubFixture) eventsOf(t *testing.T) []state.Event {
	t.Helper()
	evs, err := f.st.EventsSince(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	return evs
}

// auditsOf 读全部审计行。
func (f *hubFixture) auditsOf(t *testing.T) []state.AuditRecord {
	t.Helper()
	audits, err := f.st.RecentAudits(context.Background(), 100)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	return audits
}

func TestHubRegisterAndReplace(t *testing.T) {
	f := newHubFixture(t, nil)
	f.registerDefault(t)
	if f.hub.NodesConnected() != 1 {
		t.Fatalf("nodes connected = %d, want 1", f.hub.NodesConnected())
	}
	// 同节点新连接顶旧：连接数不变、旧连接关闭（顶替语义）。
	conn2 := newFakeConn()
	if _, err := f.hub.RegisterRelay(context.Background(), conn2, "abc123def456"); err != nil {
		t.Fatalf("RegisterRelay second: %v", err)
	}
	if f.hub.NodesConnected() != 1 {
		t.Fatalf("nodes connected after replace = %d, want 1", f.hub.NodesConnected())
	}
	eventually(t, time.Second, "old connection closed", f.conn.isClosed)
}

func TestHubRegisterUnknownHostname(t *testing.T) {
	f := newHubFixture(t, nil)
	// 容器 ID 前缀与节点 hostname 都不匹配 → 拒（成员发现）。
	if _, err := f.hub.RegisterRelay(context.Background(), newFakeConn(), "zzzz"); err == nil {
		t.Fatal("unknown hostname must be rejected (membership discovery)")
	}
	// 节点 hostname 形态（swarm 实态：任务容器 hostname = 节点 hostname）。
	rc, err := f.hub.RegisterRelay(context.Background(), newFakeConn(), "mgr")
	if err != nil {
		t.Fatalf("RegisterRelay by node hostname: %v", err)
	}
	if rc.nodeID != "n1" {
		t.Fatalf("resolved node = %q, want n1", rc.nodeID)
	}
}

func TestHubOpenSessionHappyPath(t *testing.T) {
	f := newHubFixture(t, nil)
	f.registerDefault(t)
	ctx := context.Background()
	s, err := f.hub.OpenSession(ctx, TicketBinding{TokenID: "tok1", App: "demo", AppLabel: "acme/prod/demo", Service: "web"}, 0, 0)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if f.hub.ActiveSessions() != 1 {
		t.Fatalf("active sessions = %d, want 1", f.hub.ActiveSessions())
	}
	// open 帧已发 relay（目标容器 = running task 的容器；副本细节不透出）。
	frames := f.conn.waitForWritten(t, 2*time.Second, func(frames []frameTuple) bool {
		for _, fr := range frames {
			if fr.typ == TypeSessionOpen {
				return true
			}
		}
		return false
	})
	var open *SessionOpenFrame
	for _, fr := range frames {
		if fr.typ == TypeSessionOpen {
			var o SessionOpenFrame
			if err := DecodeJSONPayload(fr.payload, &o); err != nil {
				t.Fatalf("decode open: %v", err)
			}
			if o.ID != s.ID() {
				t.Fatalf("open session id = %q, want %q", o.ID, s.ID())
			}
			open = &o
		}
	}
	if open == nil || open.ContainerID != "cafe1234567890" {
		t.Fatalf("open frame target = %+v, want newest running container cafe…", open)
	}
	// 审计 + 事件双落（terminal.opened；fail-closed 同事务的验证路径）。
	foundOpened := false
	for _, a := range f.auditsOf(t) {
		if a.Action == "terminal.opened" && a.Result == "ok" {
			foundOpened = true
		}
	}
	if !foundOpened {
		t.Fatal("terminal.opened audit row missing")
	}
	foundEvent := false
	for _, e := range f.eventsOf(t) {
		if e.Name == "terminal.opened" && e.Subject == "app:demo" {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatal("terminal.opened event row missing")
	}
}

// TestHubSessionCloseEventAndAudit relay 发来 close → 会话出表 + 关闭帧进
// 事件流 + terminal.closed 审计/事件（时长/原因元数据）。
func TestHubSessionCloseEventAndAudit(t *testing.T) {
	f := newHubFixture(t, nil)
	f.registerDefault(t)
	ctx := context.Background()
	s, err := f.hub.OpenSession(ctx, TicketBinding{TokenID: "tok1", App: "demo", AppLabel: "acme/prod/demo", Service: "web"}, 0, 0)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	// 桥接面：RelayReadLoop 在后台路由帧（relay 发来的 close → 会话收尾）。
	loopCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.hub.RelayReadLoop(loopCtx, f.rc) }()
	end, _ := EncodeCloseFrame(SessionCloseFrame{ID: s.ID(), Code: CloseOK, Reason: "remote shell exited"})
	f.conn.feed(end)
	var got *SessionCloseFrame
	select {
	case ev := <-s.Events():
		if ev.close == nil {
			t.Fatal("expected terminal event on session close")
		}
		got = ev.close
	case <-time.After(2 * time.Second):
		t.Fatal("no close event within budget")
	}
	if got.Code != CloseOK || got.Reason != "remote shell exited" {
		t.Fatalf("close event = %+v", got)
	}
	if f.hub.ActiveSessions() != 0 {
		t.Fatalf("active sessions after close = %d, want 0", f.hub.ActiveSessions())
	}
	// 审计行落库与终帧推送异步收尾——轮询等待（收尾预算 3s 内）。
	eventually(t, 3*time.Second, "terminal.closed audit row", func() bool {
		for _, a := range f.auditsOf(t) {
			if a.Action == "terminal.closed" {
				return true
			}
		}
		return false
	})
}

// TestHubSessionContentNeverLeaks 明文纪律负向断言：流经会话的 PTY 内容
// 不得出现在任何事件/审计行（payload 只允许元数据）。
func TestHubSessionContentNeverLeaks(t *testing.T) {
	f := newHubFixture(t, nil)
	f.registerDefault(t)
	ctx := context.Background()
	s, err := f.hub.OpenSession(ctx, TicketBinding{TokenID: "tok1", App: "demo", AppLabel: "acme/prod/demo", Service: "web"}, 0, 0)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.hub.RelayReadLoop(loopCtx, f.rc) }()
	// 伪装敏感内容流出（relay stdout → 会话事件面）。
	const secret = "SECRET-PTY-CONTENT-3f9a1"
	streamPayload, _ := EncodeStreamPayload(s.ID(), []byte(secret))
	f.conn.feed(mustFrame(t, TypeStdout, streamPayload))
	ev := <-s.Events()
	if ev.close != nil {
		t.Fatal("unexpected terminal event")
	}
	if string(ev.data) != secret {
		t.Fatalf("data did not reach the browser face: %q", ev.data)
	}
	// 收尾。
	end, _ := EncodeCloseFrame(SessionCloseFrame{ID: s.ID(), Code: CloseOK, Reason: "done"})
	f.conn.feed(end)
	<-s.Events()
	for _, e := range f.eventsOf(t) {
		if strings.Contains(e.Payload, secret) {
			t.Fatalf("session content leaked into event %s payload: %s", e.Name, e.Payload)
		}
	}
	for _, a := range f.auditsOf(t) {
		if strings.Contains(a.DiffSummary, secret) {
			t.Fatalf("session content leaked into audit %s", a.Action)
		}
	}
}

// TestHubSessionLimits 并发限额：per-token 2（第 3 拒）/ 全局上限（配置 3，
// 换 token 第 4 拒）；会话收尾后配额归还。
func TestHubSessionLimits(t *testing.T) {
	f := newHubFixture(t, nil)
	f.registerDefault(t)
	ctx := context.Background()
	mk := func(tokenID string) error {
		_, err := f.hub.OpenSession(ctx, TicketBinding{TokenID: tokenID, App: "demo", AppLabel: "acme/prod/demo", Service: "web"}, 0, 0)
		return err
	}
	if err := mk("t1"); err != nil {
		t.Fatalf("session 1: %v", err)
	}
	if err := mk("t1"); err != nil {
		t.Fatalf("session 2: %v", err)
	}
	// 同 token 第 3 条 → 限额拒。
	if err := mk("t1"); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("session 3 err = %v, want ErrSessionLimit", err)
	}
	// 换 token 可继续（per-token 语义）。
	if err := mk("t2"); err != nil {
		t.Fatalf("session for t2: %v", err)
	}
	// 收尾归还配额：终结 t1 的一条后，t1 可再开。会话表是 map——迭代首键
	// 随机，摘到 t2 的会话不释放 t1 配额（W3-S4 flake 诊治：全量回归偶红
	// 「session after release: terminal session limit reached (per-token
	// limit 2)」的根因），按绑定 token 钉定摘除对象。per-token 计数在
	// popSessionLocked（takeSession）内同步回滚，钉定后无异步等待面。
	var sid string
	f.hub.mu.Lock()
	for id, s := range f.hub.sessions {
		if s.binding.TokenID == "t1" {
			sid = id
			break
		}
	}
	f.hub.mu.Unlock()
	if sid == "" {
		t.Fatal("no t1 session in table (fixture broken)")
	}
	f.hub.takeSession(sid).endAfterPop("test close", CloseOK)
	if err := mk("t1"); err != nil {
		t.Fatalf("session after release: %v", err)
	}
}

// TestHubDisabled 功能开关：terminal.enabled=false → E_TERMINAL_DISABLED。
func TestHubDisabled(t *testing.T) {
	enabled := false
	f := newHubFixture(t, func(cfg *HubConfig) {
		cfg.Enabled = func() bool { return enabled }
	})
	f.registerDefault(t)
	_, err := f.hub.OpenSession(context.Background(), TicketBinding{TokenID: "t1", App: "demo", AppLabel: "acme/prod/demo", Service: "web"}, 0, 0)
	if !errors.Is(err, ErrTerminalDisabled) {
		t.Fatalf("err = %v, want ErrTerminalDisabled", err)
	}
	enabled = true
	if _, err := f.hub.OpenSession(context.Background(), TicketBinding{TokenID: "t1", App: "demo", AppLabel: "acme/prod/demo", Service: "web"}, 0, 0); err != nil {
		t.Fatalf("OpenSession after enabling: %v", err)
	}
}

// TestHubGlobalLimit 全局上限（配置面注入 3——换 token 顶满后第 4 拒）。
func TestHubGlobalLimit(t *testing.T) {
	f := newHubFixture(t, func(cfg *HubConfig) { cfg.GlobalLimit = 3 })
	f.registerDefault(t)
	ctx := context.Background()
	for i, tok := range []string{"a", "b", "c", "d"} {
		_, err := f.hub.OpenSession(ctx, TicketBinding{TokenID: tok, App: "demo", AppLabel: "acme/prod/demo", Service: "web"}, 0, 0)
		if i < 3 && err != nil {
			t.Fatalf("session %d: %v", i+1, err)
		}
		if i == 3 && !errors.Is(err, ErrSessionLimit) {
			t.Fatalf("4th session err = %v, want ErrSessionLimit", err)
		}
	}
}

// TestHubNoTaskOrNoRelay 目标选择失败面：无 running 任务 / 节点无 relay。
func TestHubNoTaskOrNoRelay(t *testing.T) {
	f := newHubFixture(t, nil)
	f.tasks.tasks["fleetly-acme-prod-demo-web"] = []TaskRuntime{
		{ID: "x", State: "shutdown", DesiredState: "shutdown"},
	}
	_, err := f.hub.OpenSession(context.Background(), TicketBinding{TokenID: "t1", App: "demo", AppLabel: "acme/prod/demo", Service: "web"}, 0, 0)
	if !errors.Is(err, ErrNoRunningTask) {
		t.Fatalf("err = %v, want ErrNoRunningTask", err)
	}
	// 任务在位但 relay 未注册（连接表空）。
	f.tasks.tasks["fleetly-acme-prod-demo-web"] = []TaskRuntime{
		{ID: "y", NodeID: "n9", ContainerID: "zzz", State: "running", DesiredState: "running", Timestamp: time.Now()},
	}
	if _, err := f.hub.OpenSession(context.Background(), TicketBinding{TokenID: "t1", App: "demo", AppLabel: "acme/prod/demo", Service: "web"}, 0, 0); !errors.Is(err, ErrNoRelayConnection) {
		t.Fatalf("err = %v, want ErrNoRelayConnection", err)
	}
}

// TestHubClusterToken 集群 token 校验：meta 哈希比对（错 token 拒——安全面
// 验收「集群 token 错误拒」的单测承载面）。
func TestHubClusterToken(t *testing.T) {
	f := newHubFixture(t, nil)
	ctx := context.Background()
	if err := f.hub.VerifyClusterToken(ctx, "anything"); err == nil {
		t.Fatal("verification must fail before provisioning")
	}
	if err := f.st.SetMeta(ctx, MetaKeyClusterTokenHash, state.HashToken("real-cluster-token")); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := f.hub.VerifyClusterToken(ctx, "wrong-token"); err == nil {
		t.Fatal("wrong cluster token must be rejected")
	}
	if err := f.hub.VerifyClusterToken(ctx, "real-cluster-token"); err != nil {
		t.Fatalf("correct token rejected: %v", err)
	}
	if err := f.hub.VerifyClusterToken(ctx, ""); err == nil {
		t.Fatal("empty token must be rejected")
	}
}

// mustFrame 是测试侧的整帧编码助手。
func mustFrame(t *testing.T, typ FrameType, payload []byte) []byte {
	t.Helper()
	frame, err := EncodeFrame(typ, payload)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	return frame
}
