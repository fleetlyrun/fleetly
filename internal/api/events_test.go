package api

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// newEventsEnv 起只含 EventsService 的 bufconn server（轮询间隔加速）。
func newEventsEnv(t *testing.T) (*testEnv, serverv1.EventsServiceClient) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := NewEventsService(st)
	svc.interval = 20 * time.Millisecond

	auth := NewAuthenticator(st)
	srv := newAuthServer(auth)
	serverv1.RegisterEventsServiceServer(srv, svc)
	conn := serveBufconn(t, srv)
	tok := tokenFor(t, st)
	return &testEnv{st: st, admTok: tok}, serverv1.NewEventsServiceClient(conn)
}

// appendEvents 注入 n 条事件，返回最后一条 seq。
func appendEvents(t *testing.T, st *state.Store, n int) int64 {
	t.Helper()
	var last int64
	for i := 0; i < n; i++ {
		err := st.InTx(context.Background(), func(tx *state.Tx) error {
			seq, err := tx.AppendEvent(context.Background(), state.Event{
				Name:    "deployment.queued",
				Subject: "deployment:test",
				Payload: "{}",
			})
			last = seq
			return err
		})
		if err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	return last
}

// collectEvents 从流收 n 帧（或超时失败）。
func collectEvents(t *testing.T, stream serverv1.EventsService_WatchEventsClient, n int) []*serverv1.WatchEventsResponse {
	t.Helper()
	var out []*serverv1.WatchEventsResponse
	deadline := time.After(5 * time.Second)
	for len(out) < n {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting frames: got %d want %d", len(out), n)
		default:
		}
		resp, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv: %v (got %d frames)", err, len(out))
		}
		out = append(out, resp)
	}
	return out
}

// TestEventsWatchSeqOrder T2.17 验收：注入 3 事件 → 客户端按 seq 升序收
// 到 3 条。
func TestEventsWatchSeqOrder(t *testing.T) {
	env, client := newEventsEnv(t)
	ctx := authCtx(context.Background(), env.admTok)
	appendEvents(t, env.st, 3)

	stream, err := client.WatchEvents(ctx, &serverv1.WatchEventsRequest{SinceSeq: 0})
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	frames := collectEvents(t, stream, 3)
	for i, f := range frames {
		ev := f.GetEvent()
		if ev == nil {
			t.Fatalf("frame %d is not an event frame: %+v", i, f)
		}
		if ev.GetSeq() != int64(i+1) {
			t.Fatalf("frame %d seq = %d, want %d", i, ev.GetSeq(), i+1)
		}
		if ev.GetName() != "deployment.queued" {
			t.Fatalf("frame %d name = %q", i, ev.GetName())
		}
	}
}

// TestEventsWatchReconnect 断线重连语义：首轮收 2 条；断线期间新事件；
// 带 since_seq=最后 seq 重连 → 只收到之后的事件（不重放、不跳号）。
func TestEventsWatchReconnect(t *testing.T) {
	env, client := newEventsEnv(t)
	ctx := authCtx(context.Background(), env.admTok)
	appendEvents(t, env.st, 2)

	stream1, err := client.WatchEvents(ctx, &serverv1.WatchEventsRequest{})
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	frames := collectEvents(t, stream1, 2)
	last := frames[len(frames)-1].GetEvent().GetSeq()
	_ = stream1.CloseSend()

	// 断线期间新事件。
	appendEvents(t, env.st, 2)

	// 重连带游标。
	stream2, err := client.WatchEvents(ctx, &serverv1.WatchEventsRequest{SinceSeq: last})
	if err != nil {
		t.Fatalf("reconnect WatchEvents: %v", err)
	}
	frames2 := collectEvents(t, stream2, 2)
	for i, f := range frames2 {
		seq := f.GetEvent().GetSeq()
		if seq != last+int64(i+1) {
			t.Fatalf("reconnect frame %d seq = %d, want %d", i, seq, last+int64(i+1))
		}
	}
}

// TestEventsWatchCursorExpired 游标过期：清空保留窗后带过期 since_seq
// 连接 → 收到一帧 cursor_expired（带 oldest_seq）后流正常关闭（EOF，
// 非 gRPC status error——信封帧取舍的验收面）。
func TestEventsWatchCursorExpired(t *testing.T) {
	env, client := newEventsEnv(t)
	ctx := authCtx(context.Background(), env.admTok)
	oldLast := appendEvents(t, env.st, 3)

	// 保留期清理：把现有事件全部清掉（模拟游标落在被清理区段）。
	if _, err := env.st.PruneExpiredEvents(context.Background(), time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("PruneExpiredEvents: %v", err)
	}
	fresh := appendEvents(t, env.st, 1)

	stream, err := client.WatchEvents(ctx, &serverv1.WatchEventsRequest{SinceSeq: oldLast - 1})
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	// 第一帧 = cursor_expired 信封帧。
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv expired frame: %v", err)
	}
	expired := resp.GetCursorExpired()
	if expired == nil {
		t.Fatalf("want cursor_expired frame, got %+v", resp)
	}
	if expired.GetOldestSeq() != fresh {
		t.Fatalf("oldest_seq = %d, want %d", expired.GetOldestSeq(), fresh)
	}
	if expired.GetMessage() == "" {
		t.Fatal("expired frame message is empty")
	}
	// 流随后正常关闭：EOF（非 status error——410 语义由信封帧承载）。
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("stream should close after expired frame")
	}
	if !errors.Is(err, io.EOF) && status.Code(err) != codes.Unknown {
		t.Fatalf("stream close err = %v, want clean EOF", err)
	}
}
