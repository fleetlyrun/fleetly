package apitest_test

// exec 子面契约测试（F3.2，ADR-0049）：受理（四件一拍——审计 Detail 行 +
// exec.session_opened 事件）、gRPC 全链（帧流/退出码）、WS 票据流
//（单用途/绑会话/坏凭证 401）、拒绝信封（无代理 E_NODE_AGENT_OFFLINE/
// 空命令非 tty/Team 并发上限 E_QUOTA_EXCEEDED）、代理凭证校验。

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// execSeed 建 project + app（exec 受理只要求 App 行在场——实例解析是
// Provider 的假面，无需部署链）。
func execSeed(t *testing.T, h *apitest.Harness, ctx context.Context) string {
	t.Helper()
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{
		ProjectId: proj.GetProject().GetId(), Name: "webapp",
	})
	require.NoError(t, err)
	return app.GetApp().GetId()
}

// TestExecSessionGRPCLink 是 gRPC 消费径全链（CLI 形态）：attach →
// CloseSend（stdin EOF）→ meta/stdout/exit 帧序 + 审计/事件落行。
func TestExecSessionGRPCLink(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	appID := execSeed(t, h, ctx)
	detach := apitest.AttachFakeExecAgent(context.Background(), h)
	defer detach()
	h.Runtime.SetExecBehavior(0, "hello\n", false)

	exec := runtimev1.NewExecServiceClient(h.Conn)
	resp, err := exec.CreateExecSession(ctx, &runtimev1.CreateExecSessionRequest{
		AppId: appID, Process: "web", Command: []string{"echo", "hi"}, Tty: false,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetTicket())
	assert.Equal(t, int32(60), resp.GetExpiresIn())
	assert.Equal(t, apitest.FakeExecInstance, resp.GetSession().GetInstance())
	assert.Equal(t, apitest.FakeExecNode, resp.GetSession().GetNodeId())

	stream, err := exec.StreamExecSession(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&runtimev1.StreamExecSessionRequest{
		Frame: &runtimev1.StreamExecSessionRequest_AttachSessionId{AttachSessionId: resp.GetSession().GetId()},
	}))
	require.NoError(t, stream.CloseSend()) // 非交互形态：stdin 即 EOF

	var gotStdout, gotStderr string
	var exitCode *int32
	var metaSeen bool
	for exitCode == nil {
		frame, err := stream.Recv()
		require.NoError(t, err)
		switch f := frame.GetFrame().(type) {
		case *runtimev1.StreamExecSessionResponse_Meta:
			metaSeen = true
			assert.Equal(t, apitest.FakeExecInstance, f.Meta.GetInstance())
		case *runtimev1.StreamExecSessionResponse_Stdout:
			gotStdout += string(f.Stdout)
		case *runtimev1.StreamExecSessionResponse_Stderr:
			gotStderr += string(f.Stderr)
		case *runtimev1.StreamExecSessionResponse_Exit:
			code := f.Exit.GetCode()
			exitCode = &code
		case *runtimev1.StreamExecSessionResponse_Error:
			t.Fatalf("unexpected error frame: %s: %s", f.Error.GetCode(), f.Error.GetMessage())
		}
	}
	assert.True(t, metaSeen, "meta frame must be first")
	assert.Equal(t, "hello\nargv: echo hi\n", gotStdout)
	assert.Empty(t, gotStderr)
	assert.Zero(t, *exitCode)

	// 会话退出后注册表清空（ExecSessionByID 查无）。
	assert.Eventually(t, func() bool {
		_, ok := h.Engine.ExecSessionByID(resp.GetSession().GetId())
		return !ok
	}, 2*time.Second, 20*time.Millisecond)

	// 审计：exec.session 行带 Detail（进程/实例/节点/命令）。
	entries, err := h.Services.Audits.List(context.Background(), h.DB.Runner(), 10)
	require.NoError(t, err)
	found := false
	for _, e := range entries {
		if e.Action != "exec.session" {
			continue
		}
		found = true
		assert.Equal(t, "app/"+appID, e.Resource)
		var d map[string]any
		require.NoError(t, json.Unmarshal([]byte(e.Detail), &d))
		assert.Equal(t, "web", d["process"])
		assert.Equal(t, apitest.FakeExecInstance, d["instance"])
		assert.Equal(t, []any{"echo", "hi"}, d["command"])
	}
	assert.True(t, found, "exec.session audit row must exist")

	// 事件：exec.session_opened 载荷带命令面。
	evts, err := h.Services.OutboxEvents.ListAfter(context.Background(), h.DB.Runner(), 0, 50)
	require.NoError(t, err)
	seen := false
	for _, ev := range evts {
		if ev.Name != "exec.session_opened" {
			continue
		}
		seen = true
		assert.Contains(t, string(ev.Payload), `"command":["echo","hi"]`)
	}
	assert.True(t, seen, "exec.session_opened event must exist")
}

// TestExecCreateRejections 钉受理拒绝信封：无代理（E_NODE_AGENT_OFFLINE）、
// 空命令非 tty（E_INVALID_ARGUMENT）、未知 App（E_NOT_FOUND）。
func TestExecCreateRejections(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	appID := execSeed(t, h, ctx)
	exec := runtimev1.NewExecServiceClient(h.Conn)

	// 无在连代理 → E_NODE_AGENT_OFFLINE。
	_, err := exec.CreateExecSession(ctx, &runtimev1.CreateExecSessionRequest{
		AppId: appID, Process: "web", Command: []string{"true"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NODE_AGENT_OFFLINE")

	// 空 argv 非 tty → E_INVALID_ARGUMENT。
	detach := apitest.AttachFakeExecAgent(context.Background(), h)
	defer detach()
	_, err = exec.CreateExecSession(ctx, &runtimev1.CreateExecSessionRequest{
		AppId: appID, Process: "web", Tty: false,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")

	// 未知 App → E_NOT_FOUND。
	_, err = exec.CreateExecSession(ctx, &runtimev1.CreateExecSessionRequest{
		AppId: "01JNOPE000000000000000000", Process: "web", Command: []string{"true"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
}

// TestExecStreamWSTicketFlow 是 Console 消费径：票据换 WS 流（单用途、
// 绑会话）、坏票据 401、帧信封字节形态。
func TestExecStreamWSTicketFlow(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	appID := execSeed(t, h, ctx)
	detach := apitest.AttachFakeExecAgent(context.Background(), h)
	defer detach()
	h.Runtime.SetExecBehavior(7, "", false)
	h.Runtime.SetExecSkipStdin(true) // WS 客户端无 half-close：自退出形态

	exec := runtimev1.NewExecServiceClient(h.Conn)
	resp, err := exec.CreateExecSession(ctx, &runtimev1.CreateExecSessionRequest{
		AppId: appID, Process: "web", Command: []string{"false"},
	})
	require.NoError(t, err)
	sessID, ticket := resp.GetSession().GetId(), resp.GetTicket()

	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, nil, fleetlygrpc.NewExecStreamSource(h.Services))
	require.NoError(t, err)

	// 无票据/坏票据 → 401（对匿名面只呈现 bad_ticket）。
	for _, q := range []string{"", "session_id=" + sessID, "session_id=" + sessID + "&ticket=bogus"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/v1/exec/stream?"+q, nil))
		assert.Equal(t, 401, rec.Code, "query %q", q)
	}

	// 票据绑定错会话 → 401（payload 绑定面）。
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/v1/exec/stream?session_id=01JWRONG00000000000000000&ticket="+ticket, nil))
	assert.Equal(t, 401, rec.Code)

	srv := httptest.NewServer(handler)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/exec/stream?session_id=" + sessID + "&ticket=" + ticket

	conn, dialResp, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil && dialResp != nil && dialResp.Body != nil {
		_ = dialResp.Body.Close() // 失败路径才需关（成功路径 Body 为 nil）
	}
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "done") //nolint:errcheck

	// 帧序：meta(0x07) → stdout(0x02) → exit(0x05 code=7)。stdin 帧形态
	// 兼容（非 tty 假执行读 stdin 到 EOF——不发送即由收口路径关闭）。
	var stdout string
	var exitCode *int
	for exitCode == nil {
		mt, data, err := conn.Read(context.Background())
		require.NoError(t, err)
		require.Equal(t, websocket.MessageBinary, mt)
		require.NotEmpty(t, data)
		switch data[0] {
		case 0x07:
			var m struct {
				Instance string `json:"instance"`
				NodeID   string `json:"node_id"`
			}
			require.NoError(t, json.Unmarshal(data[1:], &m))
			assert.Equal(t, apitest.FakeExecInstance, m.Instance)
		case 0x02:
			stdout += string(data[1:])
		case 0x05:
			var e struct {
				Code int `json:"code"`
			}
			require.NoError(t, json.Unmarshal(data[1:], &e))
			exitCode = &e.Code
		case 0x06:
			t.Fatalf("unexpected error frame: %s", data)
		}
	}
	assert.Contains(t, stdout, "argv: false")
	assert.Equal(t, 7, *exitCode)

	// 单用途：同票据第二次 → 401。
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/v1/exec/stream?session_id="+sessID+"&ticket="+ticket, nil))
	assert.Equal(t, 401, rec.Code)
}

// TestExecTeamLimit 钉 per-Team 并发上限（8）：tty 会话持 stdin 开启存活，
// 第 9 个受理 → E_QUOTA_EXCEEDED；断流收口后额度回吐。
func TestExecTeamLimit(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	appID := execSeed(t, h, ctx)
	detach := apitest.AttachFakeExecAgent(context.Background(), h)
	defer detach()
	exec := runtimev1.NewExecServiceClient(h.Conn)

	// 8 个存活会话（attach 后保持 stdin 开启——假执行阻塞在读 stdin）。
	streams := make([]runtimev1.ExecService_StreamExecSessionClient, 0, 8)
	defer func() {
		for _, s := range streams {
			_ = s.CloseSend()
		}
	}()
	for i := 0; i < 8; i++ {
		resp, err := exec.CreateExecSession(ctx, &runtimev1.CreateExecSessionRequest{
			AppId: appID, Process: "web", Command: []string{"/bin/sh"}, Tty: true,
		})
		require.NoError(t, err)
		s, err := exec.StreamExecSession(ctx)
		require.NoError(t, err)
		require.NoError(t, s.Send(&runtimev1.StreamExecSessionRequest{
			Frame: &runtimev1.StreamExecSessionRequest_AttachSessionId{AttachSessionId: resp.GetSession().GetId()},
		}))
		streams = append(streams, s)
	}
	_, err := exec.CreateExecSession(ctx, &runtimev1.CreateExecSessionRequest{
		AppId: appID, Process: "web", Command: []string{"/bin/sh"}, Tty: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_QUOTA_EXCEEDED")
}

// TestRelayAgentBadToken 钉代理凭证面：坏 join token 的握手被拒（401 形态
// ——WS 升级前的最小拒绝）。
func TestRelayAgentBadToken(t *testing.T) {
	h := apitest.New(t)
	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, nil, fleetlygrpc.NewExecStreamSource(h.Services))
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/v1/relay", nil))
	assert.Equal(t, 401, rec.Code, "missing bearer is rejected before upgrade")

	srv := httptest.NewServer(handler)
	defer srv.Close()
	_, dialResp, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/relay",
		&websocket.DialOptions{HTTPHeader: badBearerHeader()})
	require.Error(t, err, "dial with a bad credential must fail")
	if dialResp != nil && dialResp.Body != nil {
		_ = dialResp.Body.Close()
	}
}

func badBearerHeader() map[string][]string {
	return map[string][]string{"Authorization": {"Bearer WRONG"}}
}
