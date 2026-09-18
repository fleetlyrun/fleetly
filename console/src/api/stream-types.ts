// 流式帧类型：gateway NDJSON 面的逐帧 JSON 投影。grpc-gateway 对
// server-streaming 的 JSON 面把每条消息包一层 result（{"result":{…}}），
// 流中途出错时最后一帧为 {"error":{…信封…}}。oneof 成员名按 UseProtoNames
// = snake_case；int64 字段（seq 等）按 proto3 JSON 映射为字符串。

import type { CursorExpiredView, EventView, LogEntryView } from "./types";

/** FollowLogsResponse 线上帧：{"result":{"entry":{…}}}。 */
export interface FollowLogsFrame {
  result?: { entry?: LogEntryView };
  error?: Record<string, unknown>;
}

/** WatchEventsResponse 线上帧：{"result":{"event":{…}|{"cursor_expired":{…}}}}。 */
export interface WatchEventsFrame {
  result?: {
    event?: EventView;
    cursor_expired?: CursorExpiredView;
  };
  error?: Record<string, unknown>;
}
