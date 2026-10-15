// Package apivalidate 是 CLI 面的 descriptor 形状校验单源（P4-1 批 1 立接
// 口，批 2 消费）：CLI 是输入适配器，请求消息与服务端拦截器同一 descriptor
// 法源（proto 上的 buf.validate 注解），校验语义不另造镜像——服务端拦下
// 什么，客户端发送前就提前报什么。归层镜像 apperrstatus 的写法：注解反读
// 与 CEL 求值是契约机制（genproto descriptor + protovalidate 运行时），
// 非 domain 语义；错误铸形走 domain/shared/apperr（E_INVALID_ARGUMENT 在
// 注册表，构造不可失败）。
package apivalidate

import (
	"errors"
	"strings"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
)

// Validator 是客户端 descriptor 形状校验器：内持 protovalidate 实例
// （CEL 运行时），按消息 descriptor 上的 buf.validate 注解执法。
type Validator struct {
	validator protovalidate.Validator
}

// NewValidator 构造校验器。protovalidate.New 仅在 CEL 环境配置级故障时
// 失败（标准规则集无外部依赖，常态不可达）——签名保留 error 让故障显式
// 上浮而非静默 panic（与 lynx-go 服务端拦截器的 initErr 兜底同义）。
func NewValidator() (*Validator, error) {
	validator, err := protovalidate.New()
	if err != nil {
		return nil, err
	}
	return &Validator{validator: validator}, nil
}

// Validate 按消息 descriptor 的 buf.validate 注解校验 msg：违规聚合为单条
// *apperr.Error（E_INVALID_ARGUMENT），message 为各 violation 的
// "<field>: <message>" 以 "; " 连接——与服务端拦截器的 status message 同构
// （lynx-go interceptor/validate.go 的拼接形态），客户端预检文案与服务端
// 拒绝文案逐字节一致。CEL 编译/求值故障（注解缺陷，服务端会以 Internal
// 拒绝）原样上浮，不伪装成参数错误。
func (v *Validator) Validate(msg proto.Message) error {
	err := v.validator.Validate(msg)
	if err == nil {
		return nil
	}
	var violations *protovalidate.ValidationError
	if !errors.As(err, &violations) {
		return err
	}
	parts := make([]string, 0, len(violations.Violations))
	for _, violation := range violations.Violations {
		parts = append(parts, violation.String())
	}
	// "%s" 格式化：文案中的 % 不走格式化语义（与 assembly 信封包装同纪律）。
	return apperr.New("E_INVALID_ARGUMENT", "%s", strings.Join(parts, "; "))
}
