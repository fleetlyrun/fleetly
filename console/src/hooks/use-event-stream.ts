// 事件流订阅 hook：WatchEvents NDJSON 流 + seq 游标断线续读（EventsPage 与
// Home 活动流共用）。游标过期（cursor_expired 帧）→ 以 oldest_seq 重拉全量；
// 重连延迟走共享指数退避（D4-③：1.5s 起 ×2、上限 30s、±20% 抖动；服务端
// 连续立即正常关流时降频并提示）。

import { useCallback, useEffect, useRef, useState } from "react";

import { ReconnectBackoff } from "@/api/backoff";
import { watchEvents } from "@/api/streams";
import type { CursorExpiredView, EventView } from "@/api/types";

const EVENT_CAP = 500;

/** 已解包（去掉 gateway result 层）的事件帧投影。 */
type InnerEventFrame = {
  event?: EventView;
  cursor_expired?: CursorExpiredView;
};

export function useEventStream() {
  const [events, setEvents] = useState<EventView[]>([]);
  const [connected, setConnected] = useState(false);
  const [notice, setNotice] = useState("");
  const cursorRef = useRef(0); // 重连续读点（仅在回调中写；渲染派生值见 lastSeq）

  // 渲染面派生游标（不读 ref）。生成类型口径：零值/缺省 seq 回落 "0"。
  const lastSeq = events.length > 0 ? (events[events.length - 1].seq ?? "0") : "0";

  const mergeFrames = useCallback((frames: InnerEventFrame[]) => {
    for (const frame of frames) {
      if (frame.cursor_expired) {
        // 断档信号：seq ≤ oldest_seq-1 已被清理——以 oldest_seq 重拉。
        //（oldest_seq 是 proto int64 的 JSON 字符串形态）
        const oldest = Number(frame.cursor_expired.oldest_seq);
        setNotice(
          `event cursor expired (oldest kept seq ${frame.cursor_expired.oldest_seq}) — resyncing`,
        );
        cursorRef.current = Math.max(0, oldest - 1);
        setEvents([]);
        continue;
      }
      if (frame.event) {
        setNotice("");
        const incoming = frame.event;
        setEvents((prev) => {
          if (prev.some((e) => e.seq === incoming.seq)) return prev;
          return [...prev, incoming].slice(-EVENT_CAP);
        });
        cursorRef.current = Number(incoming.seq ?? 0);
      }
    }
  }, []);

  useEffect(() => {
    let conn: { close(): void } | null = null;
    let timer: number | undefined;
    let disposed = false;
    const backoff = new ReconnectBackoff();

    const open = () => {
      watchEvents(cursorRef.current, {
        onFrame: (frame) => mergeFrames([frame]),
        onEnd: (err) => {
          if (disposed) return;
          setConnected(false);
          if (err && err instanceof Error && "status" in err && (err as { status?: number }).status === 401) {
            // 全局登出已由流式层统一触发（stream.ts 的 401 处置），
            // 此处仅展示并不再重连。
            setNotice("stream rejected (401)");
            return;
          }
          const delay = backoff.nextDelayMs(err === undefined);
          setNotice(
            `stream ended — reconnecting with seq cursor${
              backoff.throttled ? " (server keeps closing the stream; retries slowed)" : ""
            }…`,
          );
          timer = window.setTimeout(() => {
            if (!disposed) open();
          }, delay);
        },
      })
        .then((c) => {
          if (disposed) c.close();
          else {
            conn = c;
            backoff.markOpen();
            setConnected(true);
          }
        });
      // openNdjsonStream 恒 resolve（连接级错误一律经 onEnd 回调上抛），
      // 重连的唯一路径是上面的 onEnd 分支——无需 .catch 兜底（M9-13）。
    };
    open();

    return () => {
      disposed = true;
      if (timer !== undefined) window.clearTimeout(timer);
      conn?.close();
    };
  }, [mergeFrames]);

  // 清空视图（保留游标续读点——只影响本地展示，不断流）。
  const clear = useCallback(() => setEvents([]), []);

  return { events, connected, notice, lastSeq, clear };
}

/** 事件行 → 状态色调（名称词表派生：*.succeeded 绿 / *.failed 红 / 其余进行中蓝）。 */
export function eventTone(name: string | undefined): "green" | "red" | "neutral-blue" {
  if (name && name.endsWith(".succeeded")) return "green";
  if (name && name.endsWith(".failed")) return "red";
  return "neutral-blue";
}
