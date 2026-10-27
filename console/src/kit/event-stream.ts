import type { FrameSource, Transport } from "@lynx-go/uikit-core/kernel";
import { ApiError } from "@/api/client";
import type { EventRow, EventStreamEnd } from "@/api/streams";
import { followEvents, mintEventTicket } from "@/api/streams";

// 事件流 transport（UI v2 批3 订阅族：events 迁 uikit useSubscription，
// ADR-0026 票据面）：open 每次铸一次性票据（秒级 TTL 单用途——重连/续订
// 皆新票）→ followEvents SSE；游标 = 引擎 getCursor 投影的 EventRow.seq，
// open(cursor) 直传 after_seq（重连续订不丢帧；gapped 后 resync(last_seq)
// 从最新 seq 重订）。
//
// closeReason 分诊（onEnd.status 的 HTTP 面口径）：
// - 410（含流中 error 帧 E_EVENTS_GONE）= "gapped"：引擎停机待命，页面
//   重读 /v1/events/status 后 sub.resync(last_seq)（resyncs++ 供黄条）；
// - 0（传输层收口/断连）= 抛 kernel {kind:"network"} 交引擎退避重开新票；
// - 其他 >0（票据/凭证类失败）= "auth" 终态。

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

export function eventsTransport(): Transport<EventRow> {
  return {
    open(cursor, signal) {
      // closeReason 挂在 FrameSource 上（迭代器收口后引擎读取）。
      const reason: { value: FrameSource<EventRow>["closeReason"] } = { value: "transport" };
      return withCloseReason(pump(cursor ?? "0", signal, reason), reason);
    },
  };

  async function* pump(
    afterSeq: string,
    signal: AbortSignal,
    reason: { value: FrameSource<EventRow>["closeReason"] },
  ): AsyncGenerator<EventRow> {
    let handle: { stop: () => void } | null = null;
    const buffer: EventRow[] = [];
    let notify: (() => void) | null = null;
    let end: EventStreamEnd | null = null;
    const wake = () => {
      const pending = notify;
      notify = null;
      pending?.();
    };
    // 主动中断（pause/stop/重启接管）不经 onEnd——followEvents 内部被
    // stop 静默收流；signal 上挂唤醒，生成器才能从 await 挂起点退出走
    // finally 兜底 stop。
    const onAbort = () => wake();
    signal.addEventListener("abort", onAbort, { once: true });
    try {
      let ticket: string;
      try {
        ticket = await mintEventTicket();
      } catch (cause) {
        if (cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) {
          reason.value = "auth"; // 铸票即会话失效：终态
          return;
        }
        throw { kind: "network", cause }; // 铸票暂时失败：退避重铸
      }
      if (signal.aborted) return;
      handle = followEvents(
        ticket,
        afterSeq,
        (event) => {
          buffer.push(event);
          wake();
        },
        (stop) => {
          end = stop;
          wake();
        },
      );
      for (;;) {
        const event = buffer.shift();
        if (event !== undefined) {
          yield event;
          continue;
        }
        if (end !== null || signal.aborted) break;
        await new Promise<void>((resolve) => {
          notify = resolve;
        });
      }
      if (signal.aborted) return; // 引擎接管：closeReason 已无消费方
      // end 只在回调里赋值——断言回声明联合型，压掉 TS 的初始化收窄
      const outcome = end as EventStreamEnd | null;
      if (outcome === null) return;
      if (outcome.status === 410) {
        reason.value = "gapped"; // 断档：页面编排 resync(last_seq) 重订
        return;
      }
      if (outcome.status === 0) {
        throw { kind: "network", cause: outcome }; // 传输收口：引擎退避重开新票
      }
      reason.value = "auth"; // 其他 >0：票据/凭证类终态
    } finally {
      signal.removeEventListener("abort", onAbort);
      handle?.stop(); // 生成器被 return（引擎 abort/迭代完成）时收流
    }
  }
}
