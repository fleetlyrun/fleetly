package cmd

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/api/systemgrpc"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// startBufconnServer 在进程内起真实 gRPC server（bufconn）并注册
// SystemService（testBuildInfo 注入 → 确定性响应）。完整装配面（拦截链 +
// gateway）的进程内夹具（apitest）随部署链批次落地。
func startBufconnServer(t *testing.T) *bufconn.Listener {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	systemv1.RegisterSystemServiceServer(srv, systemgrpc.New(testBuildInfo))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis
}

// swapDial 把 CLI 的 SDK 拨号切到 bufconn 夹具（passthrough scheme 使
// 目标字符串直达 WithContextDialer，不经 DNS 解析）。
func swapDial(t *testing.T, lis *bufconn.Listener) {
	t.Helper()
	prev := dialClient
	dialClient = func(string, ...fleetly.Option) (*fleetly.Client, error) {
		return fleetly.Dial("passthrough:///bufnet", fleetly.WithDialOptions(
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		))
	}
	t.Cleanup(func() { dialClient = prev })
}

func TestGoldenStatus(t *testing.T) {
	lis := startBufconnServer(t)
	swapDial(t, lis)

	code, out, stderr := runCLI(t, "status")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	compareGolden(t, "status", out)
}

func TestGoldenStatusJSON(t *testing.T) {
	lis := startBufconnServer(t)
	swapDial(t, lis)

	code, out, stderr := runCLI(t, "status", "--json")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	compareGolden(t, "status-json", out)
}
