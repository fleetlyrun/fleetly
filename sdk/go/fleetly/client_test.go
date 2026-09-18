// TestClientPing 是 SDK 的使用示例测试：起一个最小 SystemService gRPC
// 服务（模拟 fleetlyd 的 gRPC 面），随后按典型用法 NewClient → Ping →
// 断言响应 → Close。
package fleetly_test

import (
	"context"
	"net"
	"testing"
	"time"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
	"google.golang.org/grpc"
)

// stubSystemService 是示例服务端：Ping 回应 service/version（与 fleetlyd
// 的真实实现同形，见 cmd/fleetlyd/grpc.go）。
type stubSystemService struct {
	serverv1.UnimplementedSystemServiceServer
}

func (s *stubSystemService) Ping(context.Context, *serverv1.PingRequest) (*serverv1.PingResponse, error) {
	return &serverv1.PingResponse{Service: "fleetlyd", Version: "dev"}, nil
}

func TestClientPing(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	serverv1.RegisterSystemServiceServer(srv, &stubSystemService{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	client, err := fleetly.NewClient(fleetly.WithAddr(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.Ping(ctx)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if resp.GetService() != "fleetlyd" {
		t.Fatalf("service = %q, want %q", resp.GetService(), "fleetlyd")
	}
	if resp.GetVersion() == "" {
		t.Fatal("version is empty")
	}
}
