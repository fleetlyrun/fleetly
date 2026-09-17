package main

import (
	"context"
	"net"
	"strings"

	serverv1 "github.com/edgesets/edgefleet/genproto/edgefleet/server/v1"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
)

// newGatewayMux 构建 grpc-gateway mux：REST /v1/** 反向代理到本进程 gRPC
// （RegisterSystemServiceHandlerFromEndpoint，torchwood 同款 FromEndpoint
// 循环形态，服务增多时逐行追加注册）。返回的 mux 直接作为 lynx HTTP 服务的
// 根 handler，与 lynxhttp.Server 自挂的 /healthz/** 共存（healthz 不经此 mux）。
//
// 错误处理：自定义 HTTPErrorHandler（newGatewayErrorHandler，阶段 3/T0.2
// 落地）——gRPC 错误统一渲染为 edgefleet.shared.v1.ErrorResponse 信封
// （snake_case 七字段），与 buf.gen.yaml disable_default_errors=true 对齐
// （默认 rpcStatus 错误体已从 OpenAPI 移除）。
func newGatewayMux(grpcEndpoint string) (*runtime.ServeMux, error) {
	mux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, newJSONMarshaler()),
		runtime.WithMarshalerOption("*/*", newJSONMarshaler()),
		runtime.WithMarshalerOption("application/json", newJSONMarshaler()),
		runtime.WithErrorHandler(newGatewayErrorHandler()),
	)
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if err := serverv1.RegisterSystemServiceHandlerFromEndpoint(context.Background(), mux, grpcEndpoint, opts); err != nil {
		return nil, err
	}
	return mux, nil
}

// newJSONMarshaler 是 gateway 的 JSON marshaler：UseProtoNames 使字段名按
// proto 声明输出（snake_case，与 buf.gen.yaml 的 json_names_for_fields=false
// 及 swagger 声明一致）；EmitUnpopulated=false 时零值字段不输出（torchwood
// CustomMarshaler 同款语义）；DiscardUnknown 兼容客户端多发字段。
func newJSONMarshaler() *runtime.JSONPb {
	return &runtime.JSONPb{
		MarshalOptions: protojson.MarshalOptions{
			UseProtoNames:   true,
			EmitUnpopulated: false,
		},
		UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: true},
	}
}

// grpcEndpointFromAddr 从 grpc.addr 推导 gateway 转发目标（torchwood 同款）：
// 保留原主机（默认回环），仅补齐缺失的 host/端口默认值。
func grpcEndpointFromAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		if addr != "" && !strings.HasPrefix(addr, ":") {
			return addr
		}
		return defaultGRPCAddr
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
