package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	gohttp "net/http"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/lynx-go/lynx"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"
	lynxhttp "github.com/lynx-go/lynx/server/http"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// discardLogger 与装配形态同构的静默 logger（测试不刷屏）。
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// noCheckers 与装配形态同构的空健康检查器取值。
func noCheckers() []lynx.Checker { return nil }

// TestPingGRPCToREST 是 T0.3 双面集成测试：同进程按生产装配形态起
// ① lynx gRPC 服务（注册 SystemService + protovalidate 拦截器）与
// ② lynx HTTP 服务（根 handler = grpc-gateway mux，反代到该 gRPC），
// 验证同一 proto 契约的两面：
//   - gRPC 直连 Ping：service == "fleetlyd"、version 非空；
//   - REST GET /v1/system/ping：200，JSON 字段为 proto 声明名（snake_case：
//     service/version 为单词字段，camelCase 不可辨，故对原始响应体做精确
//     键集断言），并与 gRPC 面结果一致；
//   - /healthz/liveness 在同一 HTTP 端口仍 200（与 gateway 路由共存，
//     T0.1 能力未破坏）。
func TestPingGRPCToREST(t *testing.T) {
	// --- gRPC 面：与 NewGRPCServer 同构（lynx.App 仅提供 logger/checkers，
	// 测试按 main_test.go 先例直接以同参构造）。---
	validator, err := protovalidate.New()
	if err != nil {
		t.Fatalf("protovalidate.New: %v", err)
	}
	gs := lynxgrpc.NewServer(
		lynxgrpc.WithAddr("127.0.0.1:0"),
		lynxgrpc.WithLogger(discardLogger()),
		lynxgrpc.WithHealthCheckers(noCheckers),
		lynxgrpc.WithInterceptors(validateUnaryInterceptor(validator)),
	)
	// SystemService 组件集为空快照（Ping/双面测试不依赖健康汇总面）。
	serverv1.RegisterSystemServiceServer(gs.GetServer(), &SystemService{
		components: func() []namedHealthComponent { return nil },
	})
	if err := gs.Init(nil); err != nil {
		t.Fatalf("grpc Init: %v", err)
	}
	gsErr := make(chan error, 1)
	go func() { gsErr <- gs.Start(context.Background()) }()
	t.Cleanup(func() {
		if err := gs.Stop(context.Background()); err != nil {
			t.Errorf("grpc Stop: %v", err)
		}
		<-gsErr
	})
	select {
	case <-gs.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("grpc server not ready within 5s")
	}
	grpcAddr := gs.Addr()
	if grpcAddr == "" {
		t.Fatal("grpc server did not report a listen address")
	}

	// --- HTTP 面：newGatewayMux 是生产构造原样复用。---
	gw, err := newGatewayMux(grpcAddr)
	if err != nil {
		t.Fatalf("newGatewayMux: %v", err)
	}
	hs := lynxhttp.NewServer(gw,
		lynxhttp.WithAddr("127.0.0.1:0"),
		lynxhttp.WithHealthCheckers(noCheckers),
		lynxhttp.WithLogger(discardLogger()),
	)
	if err := hs.Init(nil); err != nil {
		t.Fatalf("http Init: %v", err)
	}
	hsErr := make(chan error, 1)
	go func() { hsErr <- hs.Start(context.Background()) }()
	t.Cleanup(func() {
		if err := hs.Stop(context.Background()); err != nil {
			t.Errorf("http Stop: %v", err)
		}
		<-hsErr
	})
	select {
	case <-hs.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("http server not ready within 5s")
	}
	httpAddr := "http://" + hs.Addr()
	client := &gohttp.Client{Timeout: 3 * time.Second}

	// --- 面 1：gRPC 直连（SDK 同款调用路径）。---
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gResp, err := serverv1.NewSystemServiceClient(conn).Ping(ctx, &serverv1.PingRequest{})
	if err != nil {
		t.Fatalf("gRPC Ping: %v", err)
	}
	if gResp.GetService() != "fleetlyd" {
		t.Fatalf("gRPC service = %q, want %q", gResp.GetService(), "fleetlyd")
	}
	if gResp.GetVersion() == "" {
		t.Fatal("gRPC version is empty")
	}

	// --- 面 2：REST /v1/system/ping（gateway → 本进程 gRPC）。---
	resp, err := client.Get(httpAddr + "/v1/system/ping")
	if err != nil {
		t.Fatalf("GET /v1/system/ping: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != gohttp.StatusOK {
		t.Fatalf("GET /v1/system/ping: status = %d, body = %s", resp.StatusCode, body)
	}
	// snake_case 断言：原始 JSON 键集恰为 proto 字段声明名
	//（service/version；camelCase 写法与之一致，以键集全等排除多余/改名键）。
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(raw) != 2 {
		t.Fatalf("keys = %v, want exactly [service version]", raw)
	}
	if _, ok := raw["service"]; !ok {
		t.Fatalf("key %q missing in %s (want proto field name, snake_case)", "service", body)
	}
	if _, ok := raw["version"]; !ok {
		t.Fatalf("key %q missing in %s (want proto field name, snake_case)", "version", body)
	}
	var rResp serverv1.PingResponse
	if err := json.Unmarshal(body, &rResp); err != nil {
		t.Fatalf("decode into proto: %v", err)
	}
	if rResp.GetService() != gResp.GetService() || rResp.GetVersion() != gResp.GetVersion() {
		t.Fatalf("REST %+v != gRPC %+v", &rResp, gResp)
	}

	// --- 共存：healthz 与 gateway 路由同端口。---
	hResp, err := client.Get(httpAddr + "/healthz/liveness")
	if err != nil {
		t.Fatalf("GET /healthz/liveness: %v", err)
	}
	_ = hResp.Body.Close()
	if hResp.StatusCode != gohttp.StatusOK {
		t.Fatalf("GET /healthz/liveness: status = %d, want 200", hResp.StatusCode)
	}
}

// TestValidateExemptPredicate 校验 protovalidate 拦截器的豁免判定：框架
// 服务（health/reflection）豁免、业务方法不豁免（Ping 经拦截器实调用已
// 在 TestPingGRPCToREST 覆盖）。
func TestValidateExemptPredicate(t *testing.T) {
	if !validateExempt("/grpc.health.v1.Health/Check") {
		t.Fatal("/grpc.health.v1.* should be exempt")
	}
	if !validateExempt("/grpc.reflection.v1.ServerReflection/ServerReflectionInfo") {
		t.Fatal("/grpc.reflection.* should be exempt")
	}
	if validateExempt("/fleetly.server.v1.SystemService/Ping") {
		t.Fatal("business methods must not be exempt")
	}
}
