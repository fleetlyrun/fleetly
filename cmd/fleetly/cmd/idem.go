package cmd

// 创建型动词的 --idempotency-key 旗标（ADR-0024 头形态通用幂等）：非空时
// 经 metadata 透传——同键同体重放返回同一响应、同键异体 409、24h 保留。
// 不带键按普通语义调用（幂等是可选承诺）。

import (
	"context"
	"flag"

	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// idemKeyFlag 是动词内旗标值的持有形态（闭包捕获，commands.Flagged 契约）。
type idemKeyFlag struct{ value string }

// declare 在动词旗标面上声明 --idempotency-key。
func (k *idemKeyFlag) declare(fs *flag.FlagSet) {
	fs.StringVar(&k.value, "idempotency-key", "",
		"replay-safe key: the same key and body return the same response for 24h; a different body with the same key is rejected with 409")
}

// bind 把非空键注入调用 context（空键 = 不启用幂等）。
func (k *idemKeyFlag) bind(ctx context.Context) context.Context {
	return fleetly.WithIdempotencyKey(ctx, k.value)
}
