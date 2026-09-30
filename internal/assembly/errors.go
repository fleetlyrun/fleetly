package assembly

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/lynx-go/grpcapi/gateway"

	sharedv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/shared/v1"
)

// errorBodyBuilder 实现 gateway.ErrorBodyBuilder：把 gRPC 错误渲染为
// fleetly.shared.v1.ErrorResponse 错误信封。
//
// 骨架期为机械信封：code 字段留空（错误码注册表未引入前不发明文档外
// 码），grpc code 与 error_id 落 context。errcode 注册表 + apperr 信封
// （E_ 码、处置提示、docs 链接）随守卫批次接入：服务端 *apperr.Error 经
// GRPCStatus() 以 status detail 携带完整信封，builder 优先还原 detail。
type errorBodyBuilder struct{}

// 接口契约断言（gateway.NewErrorHandler 参数类型）。
var _ gateway.ErrorBodyBuilder = errorBodyBuilder{}

func (errorBodyBuilder) Build(code codes.Code, message, errorID string) proto.Message {
	return &sharedv1.ErrorResponse{
		Code:    "",
		Message: message,
		Context: map[string]string{
			"error_id":  errorID,
			"grpc_code": code.String(),
		},
	}
}

// MapErrorCode 声明 gRPC code → 项目错误码映射；注册表接入后由 errcode
// 驱动（当前无映射）。
func (errorBodyBuilder) MapErrorCode(codes.Code) any { return nil }
