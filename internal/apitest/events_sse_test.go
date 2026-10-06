package apitest_test

// SSE 原生入口全链测试（F1.2 阶段 2 / ADR-0026）：票据换流（EventSource
// 无自定义头）、单用途、无票据 401、断档 410、流中事件以 text/event-stream
// 帧到达。

import (
	"bufio"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// sseSession 打开一条 SSE 订阅（真实票据流），返回行扫描器与收尾。
func sseSession(t *testing.T, h *apitest.Harness, ticket string, query string) (*bufio.Scanner, func()) {
	t.Helper()
	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, fleetlygrpc.NewEventStreamSource(h.Services), fleetlygrpc.NewExecStreamSource(h.Services), assembly.NewBrowseGate(h.Services))
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/v1/events/follow?ticket="+ticket+query, nil)
	rec := httptest.NewRecorder()
	// httptest.ResponseRecorder 不支持流式分帧读取——用真实 ServeHTTP + 手动
	// body 消费：recorder 全量缓冲对本测试反而方便（有界断言）。
	handler.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	sc := bufio.NewScanner(rec.Body)
	return sc, func() {}
}

func TestEventsSSETicketFlow(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	events := telemetryv1.NewEventsServiceClient(h.Conn)

	_, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)

	// 无票据 → 401（对匿名面不区分缺失/无效/已用）。
	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, fleetlygrpc.NewEventStreamSource(h.Services), fleetlygrpc.NewExecStreamSource(h.Services), assembly.NewBrowseGate(h.Services))
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, "GET", "/v1/events/follow", nil))
	assert.Equal(t, 401, rec.Code)

	// 票据换流（follow=0 有界重放）+ 单用途（同票据第二次 401）。
	tk, err := events.IssueEventTicket(ctx, &telemetryv1.IssueEventTicketRequest{})
	require.NoError(t, err)
	sc, _ := sseSession(t, h, tk.GetTicket(), "&follow=0")
	var sawProjectCreated bool
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "event: project.created") {
			sawProjectCreated = true
		}
	}
	require.True(t, sawProjectCreated, "the SSE replay must deliver project.created frames")

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequestWithContext(ctx, "GET", "/v1/events/follow?ticket="+tk.GetTicket()+"&follow=0", nil))
	assert.Equal(t, 401, rec2.Code, "a redeemed ticket must not work twice")
}

func TestEventsSSEFollowAndGone(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	events := telemetryv1.NewEventsServiceClient(h.Conn)

	_, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)

	// 断档：游标落在窗外 → 流前 410（统一错误信封带 E_EVENTS_GONE）。
	h.Clock.Advance(8 * 24 * time.Hour)
	_, err = outbox.New(h.DB.Clock()).TrimBefore(context.Background(), h.DB.Runner(),
		h.DB.Clock().Now().Add(-7*24*time.Hour))
	require.NoError(t, err)

	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, fleetlygrpc.NewEventStreamSource(h.Services), fleetlygrpc.NewExecStreamSource(h.Services), assembly.NewBrowseGate(h.Services))
	require.NoError(t, err)
	tk, err := events.IssueEventTicket(ctx, &telemetryv1.IssueEventTicketRequest{})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, "GET", "/v1/events/follow?ticket="+tk.GetTicket()+"&after_seq=1&follow=0", nil))
	require.Equal(t, 410, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"code":"E_EVENTS_GONE"`)
}
