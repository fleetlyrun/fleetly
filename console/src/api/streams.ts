// 实时流客户端（日志跟随 / 事件注视）：经 gateway 的 NDJSON 面，鉴权与
// base 解析与 api() 同源。断线重连策略在上层（页面 hook）：日志以最后
// 时间戳回放历史后重开跟随；事件以 seq 游标续读。
//
// 线上帧形态（grpc-gateway server-streaming JSON 面）：每条消息包一层
// result（{"result":{…}}，实测钉死）；流中途出错时最后一帧为
// {"error":{…信封…}}——在此解包并把 error 帧以 StreamError 上抛。

import { getToken, apiUrl } from "./client";
import { StreamError, openNdjsonStream, type StreamConnection } from "./stream";
import type { FollowLogsFrame, WatchEventsFrame } from "./stream-types";
import type { LogEntryView } from "./types";

function authHeaders(): Record<string, string> {
  const token = getToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

/** 解开 gateway 的 result 包裹；error 帧 → 抛 StreamError（终帧）。 */
export function unwrapFrame<T>(frame: { result?: T; error?: Record<string, unknown> }): T | null {
  if (frame.error !== undefined) {
    throw new StreamError(0, frame.error);
  }
  return frame.result ?? null;
}

export { StreamError };
export type { StreamConnection };

/** FollowLogs 帧处理器：已解包的 entry 投影。 */
export interface LogFollowConnection {
  close(): void;
}

export function followLogs(
  app: string,
  service: string | undefined,
  handlers: {
    onEntry: (entry: LogEntryView) => void;
    onEnd: (error?: unknown) => void;
  },
): Promise<LogFollowConnection> {
  const query: Record<string, string> = {};
  if (service) query.service = service;
  return openNdjsonStream<FollowLogsFrame>(
    apiUrl(`/apps/${encodeURIComponent(app)}/logs/stream`, query),
    authHeaders(),
    {
      onFrame: (frame) => {
        const inner = unwrapFrame(frame);
        if (inner?.entry) handlers.onEntry(inner.entry);
      },
      onEnd: handlers.onEnd,
    },
  );
}

/** WatchEvents 帧处理器：已解包的 event | cursor_expired 投影。 */
export function watchEvents(
  sinceSeq: number,
  handlers: {
    onFrame: (frame: { event?: import("./types").EventView; cursor_expired?: import("./types").CursorExpiredView }) => void;
    onEnd: (error?: unknown) => void;
  },
): Promise<StreamConnection> {
  return openNdjsonStream<WatchEventsFrame>(
    apiUrl("/events/stream", { since_seq: String(sinceSeq) }),
    authHeaders(),
    {
      onFrame: (frame) => {
        const inner = unwrapFrame(frame);
        if (inner) handlers.onFrame(inner);
      },
      onEnd: handlers.onEnd,
    },
  );
}
