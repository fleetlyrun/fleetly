import { ApiError, apiFetch, authHeaders } from "./client";

// 流式消费面（ADR-0044 决策 1）：
// - 日志 = GET /v1/logs 的 grpc-gateway ForwardResponseStream——chunked
//   NDJSON，每帧 {"result":{...}}；fetch 可带 Bearer 头。
// - 事件订阅 = ADR-0026 票据面（GET /v1/events/follow?ticket=...，服务端
//   契约对 EventSource 兼容）。帧是 SSE 具名事件（event: <name>），标准
//   EventSource 只收预注册名字——Console 用 fetch 解析帧，顺带拿到 410
//   断档状态码（EventSource 面不透出），重同步可在页内自动裁决。

export interface LogFrame {
  workload_id?: string;
  container?: string;
  node?: string;
  time?: string;
  line?: string;
}

export interface EventRow {
  seq?: string;
  name?: string;
  aggregate?: string;
  aggregate_id?: string;
  payload?: string;
  created_at?: string;
}

// streamDeploymentWait 消费单部署等待流（ADR-0044 决策 1 预留的 F3.1
// 单部署跟踪面）：NDJSON 帧 {"result":{"deployment":{...}}}，终态帧后
// 服务端收流（resolve 带 ended=true；传输层收口/中断 = ended=false，
// 调用方自行重开——详情页的重开按钮面）。
export async function streamDeploymentWait(
  deploymentId: string,
  signal: AbortSignal,
  onFrame: (deployment: unknown) => void,
): Promise<{ ended: boolean }> {
  const res = await fetch(`/v1/deployments/${encodeURIComponent(deploymentId)}/wait`, { signal, headers: authHeaders() });
  if (!res.ok || !res.body) {
    let code = "unknown";
    let message = `${res.status} ${res.statusText}`.trim();
    try {
      const body = (await res.json()) as { code?: string; message?: string };
      if (body.code) code = body.code;
      if (body.message) message = body.message;
    } catch {
      // 非 JSON 错误体：保留状态行口径。
    }
    throw new ApiError(res.status, code, message);
  }
  await readLines(res.body, (line) => {
    if (line === "") return;
    const frame = JSON.parse(line) as { result?: { deployment?: unknown } };
    if (frame.result?.deployment != null) onFrame(frame.result.deployment);
  });
  return { ended: !signal.aborted };
}

// streamLogs 消费 NDJSON 帧流直至服务端收流或 abort；onFrame 逐帧回调。
export async function streamLogs(
  query: URLSearchParams,
  signal: AbortSignal,
  onFrame: (frame: LogFrame) => void,
): Promise<void> {
  const res = await fetch(`/v1/logs?${query.toString()}`, { signal, headers: authHeaders() });
  if (!res.ok || !res.body) {
    let code = "unknown";
    let message = `${res.status} ${res.statusText}`.trim();
    try {
      const body = (await res.json()) as { code?: string; message?: string };
      if (body.code) code = body.code;
      if (body.message) message = body.message;
    } catch {
      // 非 JSON 错误体：保留状态行口径。
    }
    throw new ApiError(res.status, code, message);
  }
  await readLines(res.body, (line) => {
    if (line !== "") onFrame((JSON.parse(line) as { result?: LogFrame }).result ?? {});
  });
}

// mintEventTicket 经 Bearer 换一次性订阅票据（秒级 TTL、单用途）。
export async function mintEventTicket(): Promise<string> {
  const res = await apiFetch<{ ticket?: string }>("/v1/events/ticket", { method: "POST" });
  if (!res.ticket) throw new ApiError(0, "bad_ticket_response", "the ticket response carries no ticket");
  return res.ticket;
}

export interface EventStreamEnd {
  // status 是流结束的 HTTP 面口径：410 = 断档需重同步；0 = 传输层收口
  //（服务端关停/断连，调用方退避重连）；>0 其他值 = 票据/凭证类失败。
  status: number;
  // code 是流中 error 帧携带的 apperr 码（如 E_EVENTS_GONE）。
  code?: string;
}

// followEvents 用票据开事件流并解析 SSE 帧。返回 stop 供页内暂停/卸载。
export function followEvents(
  ticket: string,
  afterSeq: string,
  onEvent: (event: EventRow) => void,
  onEnd: (end: EventStreamEnd) => void,
): { stop: () => void } {
  const controller = new AbortController();
  void (async () => {
    try {
      const query = new URLSearchParams({ ticket, after_seq: afterSeq, follow: "1" });
      const res = await fetch(`/v1/events/follow?${query.toString()}`, {
        signal: controller.signal,
        headers: authHeaders(),
      });
      if (!res.ok || !res.body) {
        onEnd({ status: res.status });
        return;
      }
      await readSSEFrames(res.body, (frame) => {
        if (frame.data === undefined) return;
        if (frame.event === "error") {
          onEnd({ status: 410, code: JSON.parse(frame.data) as string });
          controller.abort();
          return;
        }
        onEvent(JSON.parse(frame.data) as EventRow);
      });
      if (!controller.signal.aborted) onEnd({ status: 0 });
    } catch {
      if (!controller.signal.aborted) onEnd({ status: 0 });
    }
  })();
  return { stop: () => controller.abort() };
}

interface SSEFrame {
  event?: string;
  data?: string;
}

// readLines 按行回调消费流（NDJSON 面；行含结尾换号已剥）。
async function readLines(body: ReadableStream<Uint8Array>, onLine: (line: string) => void): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let newline = buffer.indexOf("\n");
    while (newline >= 0) {
      onLine(buffer.slice(0, newline).trimEnd());
      buffer = buffer.slice(newline + 1);
      newline = buffer.indexOf("\n");
    }
  }
}

// readSSEFrames 按空行切帧解析 id/event/data 字段（": ping" 注释帧跳过；
// data 多行拼接——服务端单行，容错面留给规范形态）。
async function readSSEFrames(body: ReadableStream<Uint8Array>, onFrame: (frame: SSEFrame) => void): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let boundary = buffer.indexOf("\n\n");
    while (boundary >= 0) {
      const raw = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const frame = parseSSEFrame(raw);
      if (frame) onFrame(frame);
      boundary = buffer.indexOf("\n\n");
    }
  }
}

function parseSSEFrame(raw: string): SSEFrame | undefined {
  const frame: SSEFrame = {};
  const dataLines: string[] = [];
  for (const line of raw.split("\n")) {
    if (line.startsWith(":")) continue;
    if (line.startsWith("id: ")) continue;
    if (line.startsWith("event: ")) frame.event = line.slice("event: ".length);
    if (line.startsWith("data: ")) dataLines.push(line.slice("data: ".length));
  }
  if (dataLines.length > 0) frame.data = dataLines.join("\n");
  return frame.event !== undefined || frame.data !== undefined ? frame : undefined;
}
