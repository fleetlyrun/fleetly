package governance

// 流式冻结执法测试（ADR-0019 附录 A.2，F1.10）：封禁面 client-streaming
// 动词（UploadSource）的首帧 RecvMsg 包装——冻结拒绝以首帧读取错误面呈现，
// handler 零写入即返回；非封禁面方法零开销直通。

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/state/freeze"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

// fakeRecvStream 是最小 ServerStream 假底座（帧队列，尽则 io.EOF）。
type fakeRecvStream struct {
	grpc.ServerStream
	frames []proto.Message
}

func (s *fakeRecvStream) Context() context.Context { return context.Background() }

func (s *fakeRecvStream) RecvMsg(m any) error {
	if len(s.frames) == 0 {
		return io.EOF
	}
	next := s.frames[0]
	s.frames = s.frames[1:]
	proto.Merge(m.(proto.Message), next)
	return nil
}

// callStream 经流式拦截器跑一次 handler：handler 先读首帧再读后续帧，
// reads 是成功读取的帧数（拒绝面=0）。
func callStream(t *testing.T, g *FreezeGuard, method string, frames ...proto.Message) (reads int, err error) {
	t.Helper()
	ss := &fakeRecvStream{frames: frames}
	icc := g.Stream()
	called := false
	err = icc(nil, ss, &grpc.StreamServerInfo{FullMethod: method},
		func(_ any, wrapped grpc.ServerStream) error {
			called = true
			for {
				var m deliveryv1.UploadSourceRequest
				if rerr := wrapped.RecvMsg(&m); rerr != nil {
					if rerr == io.EOF {
						return nil
					}
					return rerr
				}
				reads++
			}
		})
	if err == nil {
		require.True(t, called, "handler must run when not frozen")
	}
	return reads, err
}

func TestFreezeStreamFirstFrameEnforcement(t *testing.T) {
	db, clock := statertest.New(t)
	ctx := context.Background()
	run := db.Runner()

	const pidA = "01JD0PROJA0000000000000000"
	require.NoError(t, project.New(clock).Create(ctx, run, &project.Project{ID: pidA, TeamID: tTeamA, Name: "alpha"}))
	freezes := freeze.New(clock)
	g := NewFreezeGuard(db, slog.New(slog.DiscardHandler))

	const uploadMethod = "/fleetly.delivery.v1.BuildsService/UploadSource"
	meta := func(pid string) *deliveryv1.UploadSourceRequest {
		return &deliveryv1.UploadSourceRequest{Part: &deliveryv1.UploadSourceRequest_Meta{
			Meta: &deliveryv1.UploadSourceMeta{ProjectId: pid},
		}}
	}
	chunk := &deliveryv1.UploadSourceRequest{Part: &deliveryv1.UploadSourceRequest_Chunk{Chunk: []byte("tar")}}

	// 冻结前：首帧+后续帧全量到达 handler。
	reads, err := callStream(t, g, uploadMethod, meta(pidA), chunk, chunk)
	require.NoError(t, err)
	assert.Equal(t, 3, reads)

	// Team A 冻结：首帧 Recv 即拒（handler 零读取——错误面在首帧呈现）。
	require.NoError(t, freezes.Create(ctx, run, &freeze.Freeze{ID: "01JDQFRZZ0000000000000000Z", TeamID: tTeamA, Reason: "upgrade window"}))
	reads, err = callStream(t, g, uploadMethod, meta(pidA), chunk, chunk)
	wantFrozen(t, err, "upgrade window")
	assert.Equal(t, 0, reads)

	// 无关 Team 放行；寻址失败（未知 project）放行（受理位拒绝）。
	reads, err = callStream(t, g, uploadMethod, meta("01JD0PROJB0000000000000000"))
	require.NoError(t, err)
	assert.Equal(t, 1, reads)

	// 非封禁面方法直通（流式读面不受影响）。
	reads, err = callStream(t, g, "/fleetly.delivery.v1.BuildsService/StreamBuildLogs", meta(pidA))
	require.NoError(t, err)
	assert.Equal(t, 1, reads)
}
