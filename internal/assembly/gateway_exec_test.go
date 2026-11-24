package assembly

// 节点中继 WS 入口的断连面单测（2026-11-24，ADR-0049 附录 C 落实）：hub
// 注销中继时经 RelayWriter.Close 断开连接。入口读循环与节点侧中继的读循
// 环都阻塞在 conn.Read——连接不断，两边都不知道自己已被注销（节点侧永不
// 重连；修复前适配器没有断连面可调用，rotate 之后 manager 的回环中继离线
// 到进程重启）。hub 语义由 application/execs 的测试承载，端到端（真中继
// 进程重连）在 e2e dind two-node 的 rotate 腿；本文件只验适配器：Close 让
// 两侧读循环都退出，对已结束的连接是空操作。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/execgrpc"
)

// relayNodesStub 是 serveRelay 消费的中继面最小实现：凭证恒合法，附着把
// 写端交出去；detach 是 hub 的注销路径（注销即断连），把断连结果回报。
type relayNodesStub struct {
	attached chan engine.RelayWriter
	detached chan error
}

var _ execgrpc.Nodes = (*relayNodesStub)(nil)

func (s *relayNodesStub) NodePlatformIDByCarrier(context.Context, string) (string, error) {
	return "", nil
}

func (s *relayNodesStub) ValidateRelayCredential(context.Context, string) error { return nil }

func (s *relayNodesStub) RelayAttach(_ context.Context, _, _, _ string, w engine.RelayWriter) (func(capability.RelayFrame), func(), error) {
	s.attached <- w
	return func(capability.RelayFrame) {}, func() { s.detached <- w.Close() }, nil
}

// newRelayServer 起一条只挂 serveRelay 的测试服务。
func newRelayServer(t *testing.T, stub *relayNodesStub) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveRelay(w, r, stub)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// dialTestRelay 拨一条中继连接并发 hello（节点侧 RunNodeRelay 的握手形态）。
func dialTestRelay(t *testing.T, ctx context.Context, url string) *websocket.Conn {
	t.Helper()
	conn, dialResp, err := websocket.Dial(ctx, url+"/v1/relay", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer fltrelay_adapter_test"}},
	})
	// 失败路径的响应体必须关（bodyclose 面）；成功路径 Body 为 nil
	//（coder/websocket 契约——连接接管了流；生产 relayOnce 同款）。
	if dialResp != nil && dialResp.Body != nil {
		_ = dialResp.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	hello, err := json.Marshal(capability.RelayHello{Type: "hello", CarrierNodeID: "carrier-adapter", RelayVersion: "t"})
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, hello))
	return conn
}

// attachTestRelay 拨号 + 等 hub 侧登记写端。
func attachTestRelay(t *testing.T, ctx context.Context, stub *relayNodesStub) (*websocket.Conn, engine.RelayWriter) {
	t.Helper()
	conn := dialTestRelay(t, ctx, newRelayServer(t, stub).URL)
	select {
	case w := <-stub.attached:
		return conn, w
	case <-ctx.Done():
		t.Fatal("relay never attached")
		return nil, nil
	}
}

// TestRelayWriterCloseTearsDownBothSides：hub 断开在连中继 → 节点侧读循环
// 报错返回（重连循环由此触发）→ 入口读循环退出并 detach → 重复断开是
// 空操作。
func TestRelayWriterCloseTearsDownBothSides(t *testing.T) {
	stub := &relayNodesStub{attached: make(chan engine.RelayWriter, 1), detached: make(chan error, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, w := attachTestRelay(t, ctx, stub)

	// 节点侧视角：读循环阻塞，hub 断开之前不得返回。
	readErr := make(chan error, 1)
	go func() {
		_, _, err := conn.Read(ctx)
		readErr <- err
	}()
	select {
	case err := <-readErr:
		t.Fatalf("relay read returned before the hub dropped the connection: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	require.NoError(t, w.Close(), "dropping a live relay connection must close cleanly")
	select {
	case err := <-readErr:
		require.Error(t, err, "the node relay's read loop must fail once the hub drops the connection — that is what makes it dial again")
	case <-ctx.Done():
		t.Fatal("node relay read still blocked after the hub closed the connection")
	}
	select {
	case err := <-stub.detached:
		require.NoError(t, err, "closing a connection that already ended must be a no-op")
	case <-ctx.Done():
		t.Fatal("gateway relay handler did not exit after the connection was closed")
	}
	require.NoError(t, w.Close(), "closing twice must stay a no-op")
}

// TestRelayWriterCloseIsNoOpAfterPeerDisconnects：对端先断（正常断线重连
// 场景）→ 入口 detach 时 hub 的注销断连不得报错（否则每次正常断线都记一条
// 断开失败告警）。
func TestRelayWriterCloseIsNoOpAfterPeerDisconnects(t *testing.T) {
	stub := &relayNodesStub{attached: make(chan engine.RelayWriter, 1), detached: make(chan error, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _ := attachTestRelay(t, ctx, stub)

	require.NoError(t, conn.Close(websocket.StatusNormalClosure, "test peer leaving"))
	select {
	case err := <-stub.detached:
		require.NoError(t, err, "a connection the peer already ended must close as a no-op")
	case <-ctx.Done():
		t.Fatal("gateway relay handler did not exit after the peer disconnected")
	}
}
