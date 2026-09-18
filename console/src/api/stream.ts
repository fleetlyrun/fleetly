// NDJSON 流式消费：gateway 对 gRPC server-streaming 的 JSON 面是换行
// 分块流（每帧一个 JSON 对象 + "\n" 分隔符，grpc-gateway JSONPb.Delimiter）。
// 解析器独立成纯函数形态（可单测），按 chunk 推进、断帧自动拼接。

/** 解析器状态机：跨 chunk 保留残帧，产出完整 JSON 帧。 */
export class NdjsonParser<T = unknown> {
  private buf = "";

  /** 推入一个文本 chunk，返回其中完整帧的数组。 */
  push(chunk: string): T[] {
    this.buf += chunk;
    const out: T[] = [];
    let idx: number;
    while ((idx = this.buf.indexOf("\n")) >= 0) {
      const line = this.buf.slice(0, idx);
      this.buf = this.buf.slice(idx + 1);
      const frame = parseFrame<T>(line);
      if (frame !== undefined) out.push(frame);
    }
    return out;
  }

  /** 流正常结束时调用：解析无结尾换行的最后一帧。 */
  flush(): T[] {
    const rest = this.buf;
    this.buf = "";
    const frame = parseFrame<T>(rest);
    return frame === undefined ? [] : [frame];
  }
}

function parseFrame<T>(line: string): T | undefined {
  const trimmed = line.trim();
  if (!trimmed) return undefined;
  return JSON.parse(trimmed) as T;
}

export interface StreamHandlers<T> {
  onFrame: (frame: T) => void;
  /** 流结束/断开（含服务端关流、网络断）。自动重连策略由调用方决定。 */
  onEnd?: (error?: unknown) => void;
}

export interface StreamConnection {
  close(): void;
}

/**
 * 打开一条 NDJSON 流（fetch + ReadableStream）。
 * - headers 由 authStore 提供 Bearer；鉴权失败（401）经 onError 上抛
 *   ApiError，由全局未授权监听接手。
 * - 返回的 connection 可主动关闭（AbortController）。
 */
export async function openNdjsonStream<T>(
  url: string,
  headers: Record<string, string>,
  handlers: StreamHandlers<T>,
): Promise<StreamConnection> {
  const controller = new AbortController();
  void consume(controller, url, headers, handlers);
  return { close: () => controller.abort() };
}

async function consume<T>(
  controller: AbortController,
  url: string,
  headers: Record<string, string>,
  handlers: StreamHandlers<T>,
) {
  const parser = new NdjsonParser<T>();
  try {
    const response = await fetch(url, {
      headers,
      signal: controller.signal,
    });
    if (!response.ok || !response.body) {
      let envelope: unknown = { message: `HTTP ${response.status}` };
      try {
        envelope = await response.json();
      } catch {
        // 保留状态码兜底信封
      }
      throw newStreamError(response.status, envelope);
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      for (const frame of parser.push(decoder.decode(value, { stream: true }))) {
        handlers.onFrame(frame);
      }
    }
    for (const frame of parser.flush()) {
      handlers.onFrame(frame);
    }
    handlers.onEnd?.();
  } catch (err) {
    if (controller.signal.aborted) return; // 主动关闭：非异常
    handlers.onEnd?.(err);
  }
}

/** 流级错误（HTTP 状态 + 信封体）；独立类型避免与 api() 的 ApiError 循环依赖。 */
export class StreamError extends Error {
  readonly status: number;
  readonly envelope: Record<string, unknown>;

  constructor(status: number, envelope: Record<string, unknown>) {
    super(String(envelope.message ?? `HTTP ${status}`));
    this.name = "StreamError";
    this.status = status;
    this.envelope = envelope;
  }
}

function newStreamError(status: number, envelope: unknown): StreamError {
  return new StreamError(
    status,
    envelope && typeof envelope === "object"
      ? (envelope as Record<string, unknown>)
      : { message: String(envelope) },
  );
}
