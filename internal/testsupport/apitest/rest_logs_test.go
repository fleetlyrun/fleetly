package apitest_test

// /v1/logs 的 REST 流形态行为锚（F2.6/ADR-0044 决策 1——Console 日志页
// 的消费契约）：server-streaming 注解面经 grpc-gateway ForwardResponseStream
// 以 chunked NDJSON 输出，每帧 {"result":{...}} 换行分隔、protojson
// snake_case、bytes 字段 base64。未带凭证 401。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	"google.golang.org/grpc/status"
)

// logFrame 是 gateway 流式信封的单帧形态（ForwardResponseStream 的
// result 包裹——Console 解帧口径与此对齐）。
type logFrame struct {
	Result struct {
		WorkloadID string `json:"workload_id"`
		Container  string `json:"container"`
		Node       string `json:"node"`
		Time       string `json:"time"`
		Line       string `json:"line"`
	} `json:"result"`
}

func TestRESTGatewayServesLogsAsNDJSONFrames(t *testing.T) {
	h := apitest.New(t)
	rest := h.REST()
	app := h.CreateApp(t, h.CreateProject(t, "shop").GetId(), "web").GetId()

	// 未带凭证：401（Bearer 面照常拦截流式注解面）。
	bare := httptest.NewRecorder()
	rest.ServeHTTP(bare, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/logs?app_id="+app, nil))
	assert.Equal(t, http.StatusUnauthorized, bare.Code)

	rec := rest.Get("/v1/logs?app_id=" + app + "&tail_lines=100")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// FakeRuntime 固定两帧（frame-1/frame-2）——逐行解帧钉 NDJSON 契约。
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	require.Len(t, lines, 2, rec.Body.String())
	var frames []logFrame
	for _, line := range lines {
		var frame logFrame
		require.NoError(t, json.Unmarshal([]byte(line), &frame), line)
		frames = append(frames, frame)
	}
	for i, frame := range frames {
		lineBytes, err := base64.StdEncoding.DecodeString(frame.Result.Line)
		require.NoError(t, err)
		assert.Equal(t, "frame-"+string(rune('1'+i)), string(lineBytes))
		assert.Equal(t, "web", frame.Result.Container)
		assert.NotEmpty(t, frame.Result.Time)
	}
}

// TestRESTLogSearchDisabledFace 钉检索径停用形态（P4-3 批 3 未配置族码
// 分流）：夹具未配 Logging（logging.addr 缺省空）→ text 检索径精确失败
// E_LOG_STORE_UNAVAILABLE 503（message 面向用户，非 E_INTERNAL 不铸
// error_id）；实时径（无 text）不受影响照常走 Runtime 子面——ADR-0040
// 双径的分界。
func TestRESTLogSearchDisabledFace(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	app := h.CreateApp(t, h.CreateProject(t, "log-off").GetId(), "web").GetId()

	// gRPC 面：Unavailable + 信封 E_LOG_STORE_UNAVAILABLE + 配置级 message。
	stream, serr := telemetryv1.NewLogsServiceClient(h.Conn).StreamLogs(ctx,
		&telemetryv1.StreamLogsRequest{AppId: app, Text: "boom"})
	require.NoError(t, serr, "stream header opens; failure surfaces on Recv")
	_, serr = stream.Recv()
	require.Error(t, serr)
	assert.Equal(t, "Unavailable", status.Code(serr).String())
	env, ok := apperrstatus.FromError(serr)
	require.True(t, ok)
	assert.Equal(t, "E_LOG_STORE_UNAVAILABLE", env.Code())
	assert.Contains(t, status.Convert(serr).Message(), "requires the managed log store",
		"config-level message is user-facing by design, not a leaked cause")
	assert.NotEmpty(t, env.Suggestion())
	assert.Empty(t, env.ErrorID(), "non-E_INTERNAL exits mint no error_id")

	// 实时径不受停用影响：无 text 过滤照常出 FakeRuntime 固定两帧。
	live, lerr := telemetryv1.NewLogsServiceClient(h.Conn).StreamLogs(ctx,
		&telemetryv1.StreamLogsRequest{AppId: app})
	require.NoError(t, lerr)
	_, lerr = live.Recv()
	require.NoError(t, lerr, "the live path must not depend on the managed log store")

	// REST 面：503 + 同信封（errcodeToHTTP 显式映射）。
	rec := h.REST().Get("/v1/logs?app_id=" + app + "&text=boom")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"code":"E_LOG_STORE_UNAVAILABLE"`)
}

// TestRESTGatewayServesLogsMultiProcess 钉多进程过滤面（ADR-0060）：
// repeated processes 重复键与逗号形皆可解析（grpc-gateway 反复出现/逗号
// 分裂双通道）；单值 process 兼容面不退役；集合不命中 = 200 + 空流
// （诚实空，非错误）。
func TestRESTGatewayServesLogsMultiProcess(t *testing.T) {
	h := apitest.New(t)
	rest := h.REST()
	app := h.CreateApp(t, h.CreateProject(t, "shop").GetId(), "web").GetId()

	cases := []struct {
		name   string
		query  string
		frames int
	}{
		{"repeated keys", "processes=web&processes=worker", 2},
		{"single legacy process", "process=web", 2},
		{"no hit is an honest empty stream", "processes=worker", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := rest.Get("/v1/logs?app_id=" + app + "&" + tc.query)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			body := strings.TrimSpace(rec.Body.String())
			if tc.frames == 0 {
				assert.Empty(t, body)
				return
			}
			lines := strings.Split(body, "\n")
			assert.Len(t, lines, tc.frames, body)
		})
	}
}
