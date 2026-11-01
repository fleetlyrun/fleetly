// facts.go 是 engineports 的取证/诊断事实契约面（P5 wave-2 随 execs 与
// metrics 两卫星拆包下沉，2026-11-01）：engine 核与卫星共享的纯函数帮手
// 与端口签名在此单源——核侧经包内别名零漂移，卫星侧直接消费（wave-1 的
// SleepCtx/LogIngestBatchFrames 同款按需最小下沉）。
package engineports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// EmitLedger 是取证事实发射端口的签名（engine 核 emitLedger 方法 = 唯一
// 生产实现：LedgerOnly 审计行，无 outbox 孪生）。P5 wave-2 随 execs 拆包
// 下沉——中继活体顶替的审计事件（node.relay_replaced）经本端口注入卫星
// （构造面绑定核方法值；单写发射面留核，ADR-0088 决策 3）。
type EmitLedger func(ctx context.Context, action, resource, detail string) error

// WorkloadID 合成平台 Workload ID（app ULID + process 名：唯一、稳定、
// 人读可分解——归属与 Drift 判定的锚，Provider 把它搬运到载体标记）。
// 公式真源在此导出：API/CLI 面按 process 过滤日志、exec 受理/实例解析与
// engine 核投影面（projection/processrestart）复用同一公式，不得各自拼
// 接（散落副本一旦漂移即静默失配）。P5 wave-2 自 engine 包下沉（execs
// 卫星首消费；engine 侧经 var 别名零漂移）。
func WorkloadID(appID, process string) string {
	return appID + "-" + process
}

// AuditDetailJSON 编码审计 Detail（结构受控；编码失败退回空串——审计行
// 仍落，动作可见性不因 Detail 缺失而丢失）。engine 核（nodes/converge）
// 与 execs 卫星（中继顶替审计）同一编码契约——P5 wave-2 下沉单源。
func AuditDetailJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// ContainerExitFact 判定观测是否容器终态事实（B2a W1b facts 地基）：异常
// 终止（failed/rejected 终态观测）或非零退出（OOM 的 137 形态落此）；正常
// 完成（退出码 0）不是异常终态，不入账。engine 核观测面（observ.go 终态
// 去抖/事实入账）与 metrics 卫星 crash-loop 进入沿评估同一判据——P5
// wave-2 下沉单源（核侧经 var 别名零漂移）。
func ContainerExitFact(ev capability.WorkloadEvent) bool {
	if ev.State == capability.WorkloadFailed {
		return true
	}
	return ev.ExitCode != nil && *ev.ExitCode != 0
}

// DiagnoseWindow 是诊断近窗口径：终态计数与事件面共用同一窗（崩溃循环
// 阈值 diagnosis.CrashLoopThreshold 在窗内计数——规则阈值与装配窗口分离，
// 窗口是装配面参数而非域知识）。engine 核诊断 facts 装配（diagnose.go）
// 与 metrics 卫星 crash-loop 计数（crashnotify.go）同一窗——P5 wave-2
// 下沉单源（核侧经常量别名零漂移）。
const DiagnoseWindow = 10 * time.Minute
