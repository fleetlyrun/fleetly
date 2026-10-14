package apitest_test

// /v1/logs 的 REST 流形态行为锚（F2.6/ADR-0044 决策 1——Console 日志页
// 的消费契约）：server-streaming 注解面经 grpc-gateway ForwardResponseStream
// 以 chunked NDJSON 输出，每帧 {"result":{...}} 换行分隔、protojson
// snake_case、bytes 字段 base64。未带凭证 401。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
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
	ctx := sdk.WithToken(context.Background(), h.Token)

	projects := structurev1.NewProjectsServiceClient(h.Conn)
	project, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: project.Project.Id, Name: "web"})
	require.NoError(t, err)

	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, nil, nil, assembly.NewBrowseGate(h.Services))
	require.NoError(t, err)

	// 未带凭证：401（Bearer 面照常拦截流式注解面）。
	bare := httptest.NewRecorder()
	handler.ServeHTTP(bare, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/logs?app_id="+app.App.Id, nil))
	assert.Equal(t, http.StatusUnauthorized, bare.Code)

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/logs?app_id="+app.App.Id+"&tail_lines=100", nil)
	req.Header.Set("Authorization", "Bearer "+h.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
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

// TestRESTGatewayServesLogsMultiProcess 钉多进程过滤面（ADR-0060）：
// repeated processes 重复键与逗号形皆可解析（grpc-gateway 反复出现/逗号
// 分裂双通道）；单值 process 兼容面不退役；集合不命中 = 200 + 空流
// （诚实空，非错误）。
func TestRESTGatewayServesLogsMultiProcess(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)

	projects := structurev1.NewProjectsServiceClient(h.Conn)
	project, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: project.Project.Id, Name: "web"})
	require.NoError(t, err)

	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, nil, nil, assembly.NewBrowseGate(h.Services))
	require.NoError(t, err)

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
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/logs?app_id="+app.App.Id+"&"+tc.query, nil)
			req.Header.Set("Authorization", "Bearer "+h.Token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
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
