package cmd

// rpc.go 是 RPC 动词前奏的共享骨架（P4-2 批 2 铸造，批 A 全量推广）：
//   - runRPC 收敛「dialFromEnv + defer cancel + defer Close + fn」的动词
//     四行样板（deploy 是首个整体消费者；批 A 完成其余动词的全量迁移，
//     动词面不再直连 dialFromEnv——dial_test.go 的拨号形态测试除外）。
//   - pageFlags 收敛 list 动词散写的 --after/--limit 游标旗标（ADR-0026
//     after_* + limit 惯例）；exemplar 为 deployments/builds/tasks list，
//     批 A 推广至旗标文案与 register 逐字等价的 seq 轴 list 动词——
//     StringVar 游标与异文案/异默认值站点按零行为漂移铁律保留手写形态。
//   - preValidateUsage 是 apivalidate.Validator 的 CLI 适配：请求与服务端
//     拦截器同一 descriptor 法源（proto 上的 buf.validate 注解），发送前
//     预检形状——服务端拦什么，客户端先报什么（UsageError，exit 64）。

import (
	"context"
	"errors"
	"flag"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apivalidate"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// runRPC 统一动词的拨号前奏：拨号（ctx 附 token/client 头/默认 deadline，
// 可经 opts 豁免）→ fn 消费连接 → defer 归还资源。返回 fn 的错误原样上抛。
// 批 A 全量迁移落地：动词统一经此拨号，直连 dialFromEnv 形态退役
// （golden 双形态零漂移为界，旗标/文案/输出逐字不变）。
func runRPC(ctx context.Context, opts []dialOption, fn func(ctx context.Context, c *fleetly.Client) error) error {
	ctx, cancel, c, err := dialFromEnv(ctx, opts...)
	if err != nil {
		return err
	}
	defer cancel()
	defer c.Close() //nolint:errcheck // 进程退出路径
	return fn(ctx, c)
}

// pageFlags 是 list 动词的游标分页旗标集（ADR-0026 after_* + limit 惯例）：
// register 把 --limit 与 after 旗标挂到动词 FlagSet（after 名按动词传参，
// 惯例恒 "after"——参数化留给未来异名轴）。归一值经 after/limit32 读出。
type pageFlags struct {
	after int64
	limit int
}

func (p *pageFlags) register(fs *flag.FlagSet, afterName string) {
	fs.Int64Var(&p.after, afterName, 0, "pagination cursor: the seq of the last row of the previous page")
	fs.IntVar(&p.limit, "limit", 50, "page size (max 200)")
}

// after 是游标原值（int64 seq 轴）。
func (p *pageFlags) cursor() int64 { return p.after }

// limit32 是页大小的 RPC 字段换形（页大小经 flag.IntVar 声明，域内换形）。
func (p *pageFlags) limit32() int32 { return int32(p.limit) } //nolint:gosec // 旗标域内钳制

// preValidate 单例态：Validator 构造只在首次预检发生（CEL 环境配置级故障
// 常态不可达——失败原样留存并在每次预检上浮，不静默放行未检请求）。
var (
	preValidateOnce sync.Once
	preValidator    *apivalidate.Validator
	preValidateErr  error
)

// preValidateUsage 以 descriptor 单源法源预检请求形状（P4-2 批 2，CLI 手
// 拷贝校验的退役通道）：违规 → UsageError（exit 64）携 descriptor 文案
// （proto 字段名形态——旗标名文本随手拷贝退役，文案契约变化逐案在
// P4-2 批 2 报告列明）；CEL 编译/求值故障（注解缺陷，服务端会以 Internal
// 拒绝）原样上浮，不伪装成本地用法错误。
func preValidateUsage(verb string, msg proto.Message) error {
	preValidateOnce.Do(func() {
		preValidator, preValidateErr = apivalidate.NewValidator()
	})
	if preValidateErr != nil {
		return preValidateErr
	}
	err := preValidator.Validate(msg)
	if err == nil {
		return nil
	}
	// 违规聚合是 *apperr.Error（E_INVALID_ARGUMENT，message = violations
	// join "; "）——usage 面只取文案，不携错误码（exit 64 已承载用法语义）。
	var e *apperr.Error
	if errors.As(err, &e) {
		return usageErr(verb, e.Message())
	}
	return err
}
