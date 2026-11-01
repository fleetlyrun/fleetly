// Package engineports 是 engine 核与卫星包共享的机制契约叶子（ADR-0088
// 决策 3 机制端口导出面的 P5 拆包形态，P5 wave-1 2026-11-01 落地）：机制
// 端口类型/签名与机制帮手在此下沉——engine 核与各卫星包（internal/
// application/<sat>/）都 import 本包，依赖图无环（本包禁 import engine
// 根与任何卫星包；transact 同款 application 环内契约叶子先例）。按需最小
// 面下沉：每颗卫星拆包波次只下沉它实际消费的契约（wave-1 仅 SleepCtx 与
// LogIngestBatchFrames；Check/WriteFact/WriteStep 写路径类型与 memo/观测
// 端口签名随首个消费它们的卫星波次下沉，transition.go 届时改引下沉类型
// =别名零漂移）。
package engineports

import (
	"context"
	"time"
)

// LogIngestBatchFrames 是 Logging.Ingest 单批帧数上限（达此即同步 flush
// ——写端在流读侧天然背压）。采集环（logpipes 卫星）与 build 出口批汇
// （engine buildlog）两路径的同一 ingest 批量契约——单定义在此（P5
// wave-1 自 engine 包 logging.go 下沉，两侧经常量别名零漂移）。
const LogIngestBatchFrames = 256

// SleepCtx 睡 d 直至完成或 ctx 取消（false = 已取消）——观测扇出退避
// （engine observ.go）与采集 goroutine 重启退避（logpipes 卫星）的共享
// 睡眠原语。select 超时不在 for 体内（loopguard 判据：helper 形态，非
// 周期环）。单定义在此（P5 wave-1 自 engine 包 observ.go 下沉，engine
// 侧经 var 别名零漂移）。
func SleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
