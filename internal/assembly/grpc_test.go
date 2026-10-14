package assembly

// 超时拦截器与收包限额的装配层测试（P1-2 / Q-11）：拦截器单元行为、
// NewInterceptors 链尾挂载、bufconn 线级 DEADLINE_EXCEEDED、5MB webhook
// payload 跨 gRPC 段到达 ReceiveWebhook（及缺省 4MiB 的反面病灶）。

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lynx-go/grpcapi/gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/idem"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statertest"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/fleetlygrpc"
)

// TestUnaryTimeoutInterceptorCancelsHandler 直接驱动拦截器：挂死形态的
// handler（只等取消）在限期内被切，错误即 ctx.DeadlineExceeded。
func TestUnaryTimeoutInterceptorCancelsHandler(t *testing.T) {
	ic := newUnaryTimeoutInterceptor(50 * time.Millisecond)
	start := time.Now()
	_, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/assembly.test/Hang"},
		func(ctx context.Context, _ any) (any, error) {
			<-ctx.Done() // 挂死形态：不看 req、只等取消
			return nil, ctx.Err()
		})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second, "handler must be cut at the deadline, not by the test timeout")
}

// TestNewInterceptorsBoundsUnaryHandler 钉住链形态：NewInterceptors 产出
// 的 unary 链尾（最内层）必须是超时拦截器——handler ctx 带 deadline 且
// 量级对齐 grpcUnaryTimeout（assembly 服务器与 apitest 夹具共用本构造，
// 链尾挂载对两面同时生效）。
func TestNewInterceptorsBoundsUnaryHandler(t *testing.T) {
	db, _ := statertest.New(t)
	policySet, err := NewPolicySet()
	require.NoError(t, err)
	unary, stream, err := NewInterceptors(slog.New(slog.DiscardHandler), policySet,
		authn.NewAuthenticator(db, policySet, ScopeResources(), slog.New(slog.DiscardHandler)),
		idem.NewEnforcer(db, slog.New(slog.DiscardHandler)),
		governance.NewRateLimiter(db.Clock(), 0, 0),
		governance.NewFreezeGuard(db, slog.New(slog.DiscardHandler)))
	require.NoError(t, err)
	require.NotEmpty(t, unary)
	require.NotEmpty(t, stream)

	sawDeadline := false
	_, err = unary[len(unary)-1](context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/assembly.test/Ping"},
		func(ctx context.Context, _ any) (any, error) {
			dl, ok := ctx.Deadline()
			sawDeadline = ok
			if ok {
				assert.WithinDuration(t, time.Now().Add(grpcUnaryTimeout), dl, 5*time.Second,
					"deadline magnitude must track grpcUnaryTimeout")
			}
			return nil, nil
		})
	require.NoError(t, err)
	assert.True(t, sawDeadline, "the innermost chain item must impose the unary request deadline")
}

// TestNewInterceptorsSanitizeAtChainHead：出口纪律拦截器挂链头（unary[0]）
// ——handler 返回的 E_INTERNAL 在拦截器层脱敏 + 铸 error_id，cause 全文落
// 服务端日志（error_id 关联）；非 E_INTERNAL 原样放行（三面行为回归在
// apitest，本用例钉链形与单点日志）。
func TestNewInterceptorsSanitizeAtChainHead(t *testing.T) {
	db, _ := statertest.New(t)
	policySet, err := NewPolicySet()
	require.NoError(t, err)
	var logBuf lockedBuffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	unary, _, err := NewInterceptors(logger, policySet,
		authn.NewAuthenticator(db, policySet, ScopeResources(), slog.New(slog.DiscardHandler)),
		idem.NewEnforcer(db, slog.New(slog.DiscardHandler)),
		governance.NewRateLimiter(db.Clock(), 0, 0),
		governance.NewFreezeGuard(db, slog.New(slog.DiscardHandler)))
	require.NoError(t, err)
	require.NotEmpty(t, unary)

	internalErr := apperr.New("E_INTERNAL", "internal error: %s", "sql: tx rolled back").
		WithCause(errors.New("driver: bad conn"))
	_, err = unary[0](context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/assembly.test/Boom"},
		func(context.Context, any) (any, error) { return nil, internalErr })
	require.Error(t, err)
	st := status.Convert(err)
	assert.Equal(t, "E_INTERNAL: internal server error", st.Message(), "chain head must serve the redacted message")
	env, ok := apperrstatus.FromError(err)
	require.True(t, ok)
	assert.Regexp(t, `^[0-9a-f]{32}$`, env.ErrorID())
	logs := logBuf.String()
	assert.Contains(t, logs, env.ErrorID())
	assert.Contains(t, logs, "sql: tx rolled back")
	assert.Contains(t, logs, "driver: bad conn")

	// 非 E_INTERNAL：零收口放行——*Error 不再自带 GRPCStatus 方法，出口
	// 单点以等价 status 形态承接（同 code/同 message/同信封 detail，wire
	// 零漂移）。
	notFound := apperr.New("E_NOT_FOUND", "app web not found")
	_, err = unary[0](context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/assembly.test/Miss"},
		func(context.Context, any) (any, error) { return nil, notFound })
	passed := status.Convert(err)
	assert.Equal(t, codes.NotFound, passed.Code())
	assert.Equal(t, "E_NOT_FOUND: app web not found", passed.Message())
	passedEnv, ok := apperrstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, "E_NOT_FOUND", passedEnv.Code())
	assert.Empty(t, passedEnv.ErrorID(), "non-E_INTERNAL exits carry no minted id")
}

// lockedBuffer 是 slog 的并发安全缓冲（TextHandler 仅在 handler 内加锁，
// 跨 goroutine 读快照需自带互斥）。
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestUnaryTimeoutEndToEnd 经 bufconn 线级验证：挂死的 unary handler 在
// 服务端超时拦截器限期内被 ctx 切断，客户端收到 DEADLINE_EXCEEDED。
func TestUnaryTimeoutEndToEnd(t *testing.T) {
	const service = "assembly.timeout.v1.Probe"
	// hangService 是手工 ServiceDesc 的实现类型锚（grpc.RegisterService 经
	// HandlerType（接口）反射校验实现类型，struct/nil 都会 panic；空接口
	// 即可满足——handler 全部逻辑收在 MethodDesc 内）。
	type hangService interface{}
	type hangImpl struct{}

	desc := &grpc.ServiceDesc{
		ServiceName: service,
		HandlerType: (*hangService)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Hang",
			// 与生成代码同款形态：方法 handler 负责把链式拦截器（第 4 参）
			// 包在真实 handler 外——跳过它则服务器级链根本不生效。
			Handler: func(_ any, ctx context.Context, _ func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				handler := func(ctx context.Context, _ any) (any, error) {
					<-ctx.Done() // 只等取消：deadline 由拦截器链注入
					return nil, ctx.Err()
				}
				if interceptor == nil {
					return handler(ctx, nil)
				}
				return interceptor(ctx, nil, &grpc.UnaryServerInfo{
					FullMethod: "/" + service + "/Hang",
				}, handler)
			},
		}},
	}
	conn := newBufconnGRPC(t, []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(newUnaryTimeoutInterceptor(150 * time.Millisecond)),
	}, func(s *grpc.Server) { s.RegisterService(desc, hangImpl{}) })

	// 客户端自身 deadline 放宽到 5s：DEADLINE_EXCEEDED 必须来自服务端拦截器
	// （客户端侧超时会以自身 5s 为准，二者可由耗时区分）。
	clientCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	err := conn.Invoke(clientCtx, "/"+service+"/Hang", &emptypb.Empty{}, &emptypb.Empty{})
	require.Error(t, err)
	assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
	assert.Less(t, time.Since(start), time.Second, "the server-side interceptor must cut the handler, not the client deadline")
}

// TestHookPayloadLimitWithinRecvLimit 是 Q-11 守卫的命名锚：webhook 上限
// 必须落在 gRPC 收包上限内（包内 init 断言同名生效，违反即启动红；本用例
// 给出可读的失败定位）。
func TestHookPayloadLimitWithinRecvLimit(t *testing.T) {
	assert.LessOrEqual(t, hookPayloadLimit, grpcMaxRecvMsgSize,
		"hookPayloadLimit must not exceed grpcMaxRecvMsgSize or large webhook payloads die in opaque RESOURCE_EXHAUSTED instead of the designed 413")
}

// recordingHooksServer 记录到达的 ReceiveWebhook 请求（payload 尺寸断言面）。
type recordingHooksServer struct {
	deliveryv1.UnimplementedHooksServiceServer
	mu   sync.Mutex
	reqs []*deliveryv1.ReceiveWebhookRequest
}

func (s *recordingHooksServer) ReceiveWebhook(_ context.Context, req *deliveryv1.ReceiveWebhookRequest) (*deliveryv1.ReceiveWebhookResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	return &deliveryv1.ReceiveWebhookResponse{Status: "accepted"}, nil
}

func (s *recordingHooksServer) received() []*deliveryv1.ReceiveWebhookRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*deliveryv1.ReceiveWebhookRequest, len(s.reqs))
	copy(out, s.reqs)
	return out
}

// TestWebhookLargePayloadTraversesGRPCHop 验收 Q-11：5MB webhook payload 经
// 原生 handler → gRPC 段 → ReceiveWebhook 全量到达（不再死于 gRPC 默认
// 4MiB 的不透明 RESOURCE_EXHAUSTED）；反面（不带 GRPCServerOptions 的
// 服务器）钉住病灶形态。
func TestWebhookLargePayloadTraversesGRPCHop(t *testing.T) {
	const size = 5 << 20 // 5MB：介于 gRPC 默认 4MiB 与设计上限 25MiB 之间
	body := strings.Repeat("x", size)

	ok := &recordingHooksServer{}
	withLimit := newBufconnGRPC(t, GRPCServerOptions(),
		func(s *grpc.Server) { deliveryv1.RegisterHooksServiceServer(s, ok) })
	rec := post(newHooksHandler(deliveryv1.NewHooksServiceClient(withLimit)), fleetlygrpc.HooksURLPrefix+"flthook_t", body, nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	got := ok.received()
	require.Len(t, got, 1)
	assert.Len(t, got[0].GetPayload(), size, "the raw payload must reach ReceiveWebhook in full")

	blocked := &recordingHooksServer{}
	without := newBufconnGRPC(t, nil,
		func(s *grpc.Server) { deliveryv1.RegisterHooksServiceServer(s, blocked) })
	rec2 := post(newHooksHandler(deliveryv1.NewHooksServiceClient(without)), fleetlygrpc.HooksURLPrefix+"flthook_t", body, nil)
	assert.Equal(t, gateway.DefaultCodeToHTTP(codes.ResourceExhausted), rec2.Code,
		"without the explicit recv limit the same request dies in the opaque gRPC default")
	assert.Empty(t, blocked.received(), "the payload must not reach ReceiveWebhook when the transport rejects it")
}

// newBufconnGRPC 起一台 bufconn gRPC 服务器并返回已连接客户端。
func newBufconnGRPC(t testing.TB, opts []grpc.ServerOption, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(64 * 1024)
	srv := grpc.NewServer(opts...)
	register(srv)
	go func() { _ = srv.Serve(lis) }() //nolint:errcheck // 测试收尾统一关闭
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
