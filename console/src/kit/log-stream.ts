import type { FrameSource, Transport } from "@lynx-go/uikit-core/kernel";
import { ApiError } from "@/api/client";
import type { LogFrame } from "@/api/streams";
import { streamLogs } from "@/api/streams";
import type { LogControls } from "@/features/logs/use-log-stream";

// 日志流 transport（UI v2 批3 订阅族：logs 迁 uikit useSubscription）：
// buildLogQuery 照抄 features/logs/use-log-stream 的参数面（app/process/
// tail/text/follow——服务端契约定格语义，三轴互斥寻址 IA v3 二期①）；
// streamLogs 的 push 回调桥接为 pull 型 FrameSource 交订阅引擎驱动。
//
// closeReason 分诊（终态归 transport，引擎只消费 reason）：
// - streamLogs resolve（服务端收流：非 follow 收完即止 / follow 服务端关流）
//   = "ended"；
// - fetch ApiError 401/403 = "auth"（会话失效终态，走全局 401 闭环）；
// - 其余错误抛 kernel {kind:"network"} 交引擎退避重连——follow 形态重开
//   同 query，服务端按 tail_lines 重放尾窗（日志无游标语义，断连窗口以
//   尾窗对齐为准，不假装补齐）。

/** EventFrame——传输帧信封：payload 原样、seq 跨重连单调递增、at 到达时刻。 */
export interface EventFrame<F> {
  seq: number;
  at: Date;
  payload: F;
}

// withCloseReason 给生成器挂上引擎读的 closeReason（getter 活性——收口
// 分诊发生在生成器体内，迭代完成后才被读取；运行时挂属性，TS 看不见）。
function withCloseReason<F>(
  iterator: AsyncGenerator<F>,
  reason: { value: FrameSource<F>["closeReason"] },
): FrameSource<F> {
  return Object.defineProperty(iterator, "closeReason", {
    get: () => reason.value,
  }) as unknown as FrameSource<F>;
}

// buildLogQuery 组 GET /v1/logs 的 query（与 use-log-stream.start 同口径：
// 三轴互斥 + processes/containers repeated 多值（空串剔除）+ tail/text
// 可选 + follow 0/1）。
export function buildLogQuery(controls: LogControls): URLSearchParams {
  const query = new URLSearchParams();
  if (controls.appId) query.set("app_id", controls.appId);
  if (controls.databaseId) query.set("database_id", controls.databaseId);
  if (controls.runId) query.set("run_id", controls.runId);
  for (const process of controls.processes) {
    if (process !== "") query.append("processes", process);
  }
  for (const container of controls.containers) {
    if (container !== "") query.append("containers", container);
  }
  if (controls.tailLines !== "") query.set("tail_lines", controls.tailLines);
  if (controls.text !== "") query.set("text", controls.text);
  query.set("follow", controls.follow ? "1" : "0");
  return query;
}

export function logsTransport(controls: LogControls): Transport<EventFrame<LogFrame>> {
  // seq 计数在 transport 闭包（引擎每次重连走同一实例的 open）——跨重连
  // 单调递增，LogViewer 行键/下载序不回绕。
  let seq = 0;
  return {
    open(_cursor, signal) {
      // closeReason 挂在 FrameSource 上（迭代器收口后引擎读取）。
      const reason: { value: FrameSource<EventFrame<LogFrame>>["closeReason"] } = { value: "transport" };
      return withCloseReason(pump(signal, reason), reason);
    },
  };

  // pump 把 streamLogs 的 push 回调桥接为 async generator（引擎 pull 节奏）。
  async function* pump(
    signal: AbortSignal,
    reason: { value: FrameSource<EventFrame<LogFrame>>["closeReason"] },
  ): AsyncGenerator<EventFrame<LogFrame>> {
    const buffer: LogFrame[] = [];
    let notify: (() => void) | null = null;
    let settled: { ok: true } | { ok: false; cause: unknown } | null = null;
    const wake = () => {
      const pending = notify;
      notify = null;
      pending?.();
    };
    const running = streamLogs(buildLogQuery(controls), signal, (frame) => {
      buffer.push(frame);
      wake();
    }).then(
      () => {
        settled = { ok: true };
        wake();
      },
      (cause: unknown) => {
        settled = { ok: false, cause };
        wake();
      },
    );
    try {
      for (;;) {
        const frame = buffer.shift();
        if (frame !== undefined) {
          yield { seq: (seq += 1), at: new Date(), payload: frame };
          continue;
        }
        if (settled !== null) break;
        await new Promise<void>((resolve) => {
          notify = resolve;
        });
      }
      // settled 只在回调里赋值——断言回声明联合型，压掉 TS 的初始化收窄
      const outcome = settled as { ok: true } | { ok: false; cause: unknown } | null;
      if (outcome !== null && !outcome.ok) {
        if (outcome.cause instanceof ApiError && (outcome.cause.status === 401 || outcome.cause.status === 403)) {
          reason.value = "auth"; // 会话失效：终态，不退避
          return;
        }
        // 传输中断：交引擎退避重连（follow 重开同 query 重放 tail）。
        throw { kind: "network", cause: outcome.cause };
      }
      reason.value = "ended"; // 非 follow 收完自然收流；follow 为服务端关流
    } finally {
      // 引擎主动 abort（pause/stop/重启接管）触发 abort → streamLogs
      // reject → settled 落定 → 挂起点唤醒，生成器得以退出走 finally；
      // 在途 promise 已挂 rejection handler，不悬挂。
      void running.catch(() => undefined);
    }
  }
}
