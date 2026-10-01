package apitest_test

// 事件订阅面全链测试（F1.2 / ADR-0026）：gRPC 流重放与跟随、断档 410 +
// 状态基准、SSE 票据铸造（SSE 原生入口的行为面在阶段 2 的 gateway 测试）。

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// codeOf 提取 apperr 信封码（无信封时给 gRPC code 兜底标签）。
func eventsCodeOf(t *testing.T, err error) string {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok)
	if e, ok := apperr.FromGRPCStatus(st); ok {
		return e.Code()
	}
	return "grpc:" + st.Code().String()
}

func TestEventsStreamReplayAndFollow(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	events := telemetryv1.NewEventsServiceClient(h.Conn)

	_, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)
	_, err = projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "lab"})
	require.NoError(t, err)

	// 重放（follow=false）：保留窗全量后 EOS。
	replay, err := events.StreamEvents(ctx, &telemetryv1.StreamEventsRequest{Follow: false})
	require.NoError(t, err)
	var names []string
	var lastSeq int64
	for {
		frame, err := replay.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		names = append(names, frame.GetEvent().GetName())
		lastSeq = frame.GetEvent().GetSeq()
	}
	require.GreaterOrEqual(t, lastSeq, int64(2))
	require.Contains(t, names, "project.created")

	// 状态基准：earliest/last 与重放所见一致。
	st, err := events.GetEventStatus(ctx, &telemetryv1.GetEventStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), st.GetEarliestSeq(), "nothing trimmed: earliest is the first row")
	assert.Equal(t, lastSeq, st.GetLastSeq())

	// 跟随（follow=true）：以 lastSeq 为游标开流，途中落新事件必须到达。
	followCtx, cancel := context.WithCancel(ctx)
	stream, err := events.StreamEvents(followCtx, &telemetryv1.StreamEventsRequest{AfterSeq: lastSeq, Follow: true})
	require.NoError(t, err)
	got := make(chan string, 4)
	go func() {
		for {
			frame, err := stream.Recv()
			if err != nil {
				close(got)
				return
			}
			got <- frame.GetEvent().GetName()
		}
	}()
	// 等待流进入轮询拍后落新事件（轮询间隔 250ms；假时钟不影响真实墙钟
	// 轮询——此处等真实 sleep 到位再触发）。
	time.Sleep(2 * streamEventsPollIntervalWall)
	_, err = projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "follow-me"})
	require.NoError(t, err)
	var seen string
	timeout := time.After(5 * time.Second)
	for seen == "" {
		select {
		case n, ok := <-got:
			if !ok {
				t.Fatal("follow stream closed before the new event arrived")
			}
			if n == "project.created" {
				seen = n
			}
		case <-timeout:
			t.Fatal("follow stream did not deliver the event appended mid-stream")
		}
	}
	cancel()

	// SSE 票据铸造（兑换面在 SSE 原生入口测试）。
	ticket, err := events.IssueEventTicket(ctx, &telemetryv1.IssueEventTicketRequest{})
	require.NoError(t, err)
	assert.NotEmpty(t, ticket.GetTicket())
	assert.Equal(t, int32(60), ticket.GetExpiresIn())
}

// streamEventsPollIntervalWall 与服务端轮询拍同值（测试同步用；服务端常量
// 在 fleetlygrpc 包内，此处取同量级）。
const streamEventsPollIntervalWall = 250 * time.Millisecond

func TestEventsGoneAfterRetentionTrim(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	events := telemetryv1.NewEventsServiceClient(h.Conn)

	_, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)
	_, err = projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "lab"})
	require.NoError(t, err)

	// 快进时钟越过保留窗并执行 retention 清扫（janitor 的同款调用）。
	h.Clock.Advance(8 * 24 * time.Hour)
	n, err := outbox.New(h.DB.Clock()).TrimBefore(context.Background(), h.DB.Runner(),
		h.DB.Clock().Now().Add(-7*24*time.Hour))
	require.NoError(t, err)
	require.Greater(t, n, int64(0), "the retention trim must have reclaimed rows")

	// 断档：游标落在窗外 → E_EVENTS_GONE（REST 410）；List 与 Stream 同口径。
	_, err = events.ListEvents(ctx, &telemetryv1.ListEventsRequest{AfterSeq: 1})
	require.Error(t, err)
	assert.Equal(t, "E_EVENTS_GONE", eventsCodeOf(t, err))

	stream, serr := events.StreamEvents(ctx, &telemetryv1.StreamEventsRequest{AfterSeq: 1, Follow: false})
	if serr == nil {
		_, serr = stream.Recv()
	}
	require.Error(t, serr)
	assert.Equal(t, "E_EVENTS_GONE", eventsCodeOf(t, serr))

	// 状态基准如实反映窗界：earliest=0（空窗）不是断档；从 0 起订阅合法。
	st, err := events.GetEventStatus(ctx, &telemetryv1.GetEventStatusRequest{})
	require.NoError(t, err)
	assert.Zero(t, st.GetEarliestSeq())
	replay, err := events.StreamEvents(ctx, &telemetryv1.StreamEventsRequest{Follow: false})
	require.NoError(t, err)
	_, err = replay.Recv()
	assert.ErrorIs(t, err, io.EOF, "empty window: from-zero subscription is legal and ends immediately")
}
