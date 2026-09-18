package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"buf.build/go/protovalidate"
	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	sharedv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/shared/v1"
	"github.com/lynx-go/lynx"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// NewGRPCServer 创建控制面 gRPC 服务（lynx server/grpc，内置恢复/日志
// 拦截器、grpc.health.v1 与反射）：注册 SystemService 实现，服务必须在
// Serve 之前注册（lynx Server 在 Start 时才 Serve，构造期注册安全）。
// 鉴权/限流拦截器随 D21 拦截器链阶段接入；本阶段只挂 protovalidate
// 形状校验（拦截链尾、handler 之前）。
func NewGRPCServer(app lynx.App, cfg *AppConfig, sys *SystemService) (*lynxgrpc.Server, error) {
	validator, err := protovalidate.New()
	if err != nil {
		return nil, fmt.Errorf("construct protovalidate validator: %w", err)
	}
	srv := lynxgrpc.NewServer(
		lynxgrpc.WithAddr(cfg.GRPCAddr()),
		lynxgrpc.WithLogger(app.Logger()),
		lynxgrpc.WithHealthCheckers(app.HealthCheckers),
		lynxgrpc.WithInterceptors(validateUnaryInterceptor(validator)),
	)
	serverv1.RegisterSystemServiceServer(srv.GetServer(), sys)
	return srv, nil
}

// SystemService 实现 server.v1.SystemService（T0.3 契约验证面）。
type SystemService struct {
	serverv1.UnimplementedSystemServiceServer
}

// NewSystemService 构造 SystemService（无状态）。
func NewSystemService() *SystemService { return &SystemService{} }

// Ping 回应 service / version；version 由构建 -ldflags 注入（main.go），
// 未注入时为 "dev"。proto 字段上的 buf.validate 最小约束由拦截器统一校验。
func (s *SystemService) Ping(ctx context.Context, req *serverv1.PingRequest) (*serverv1.PingResponse, error) {
	return &serverv1.PingResponse{Service: "fleetlyd", Version: version}, nil
}

// validateExemptPrefixes 豁免框架服务：health/reflection 请求不带
// buf.validate 规则，跳过以求值零开销（torchwood 同款白名单）。
var validateExemptPrefixes = []string{
	"/grpc.health.v1.",
	"/grpc.reflection.",
}

// validateUnaryInterceptor 按 proto 上的 buf.validate 注解（protovalidate
// CEL 运行时求值）对请求消息做形状校验：形状约束在 proto 声明、在此统一
// 生效；跨字段与业务规则仍留在 app 用例层。仅 unary，与现有服务面一致。
func validateUnaryInterceptor(validator protovalidate.Validator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if validator == nil || validateExempt(info.FullMethod) {
			return handler(ctx, req)
		}
		msg, ok := req.(proto.Message)
		if !ok {
			// 非 proto 消息（理论不可达）不校验，交由后续链路处理。
			return handler(ctx, req)
		}
		if err := validator.Validate(msg); err != nil {
			return nil, validateStatusError(err)
		}
		return handler(ctx, req)
	}
}

// validateStatusError 映射校验错误为带 ErrorResponse 信封 detail 的 status：
//   - 规则违规 → InvalidArgument；不新造错误码（清单外码须 T0.5 冻结），
//     信封 code 留空（gateway 侧按退化信封渲染），违规明细进 message 与
//     context（键 = 字段路径，值 = 规则 ID + 说明）；
//   - CEL 编译/求值故障 → Internal（注解缺陷属服务端 bug，fail-closed
//     拒绝而非放行未校验请求），同样携带信封 detail。
func validateStatusError(err error) error {
	var violations *protovalidate.ValidationError
	if errors.As(err, &violations) {
		parts := make([]string, 0, len(violations.Violations))
		ctx := make(map[string]string, len(violations.Violations))
		for _, violation := range violations.Violations {
			parts = append(parts, violation.String())
			field := ""
			if violation.Proto != nil {
				field = protovalidate.FieldPathString(violation.Proto.GetField())
			}
			if field == "" {
				field = fmt.Sprintf("violation.%d", len(ctx))
			}
			ctx[field] = violation.Proto.GetRuleId() + ": " + violation.Proto.GetMessage()
		}
		return statusWithEnvelope(codes.InvalidArgument, strings.Join(parts, "; "), ctx)
	}
	msg := "request validation rules failed to evaluate: " + err.Error()
	return statusWithEnvelope(codes.Internal, msg, nil)
}

// statusWithEnvelope 构造携带 ErrorResponse detail 的 status（信封 code
// 留空、message 承载文案；detail 附加失败时退化为无 detail status，gateway
// 走退化信封路径）。
func statusWithEnvelope(c codes.Code, message string, context map[string]string) error {
	st := status.New(c, message)
	withDetail, derr := st.WithDetails(&sharedv1.ErrorResponse{Message: message, Context: context})
	if derr != nil {
		return st.Err()
	}
	return withDetail.Err()
}

func validateExempt(fullMethod string) bool {
	for _, prefix := range validateExemptPrefixes {
		if strings.HasPrefix(fullMethod, prefix) {
			return true
		}
	}
	return false
}
