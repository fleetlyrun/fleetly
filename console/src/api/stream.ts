// NDJSON 流式消费：gateway 对 gRPC server-streaming 的 JSON 面是换行
// 分块流（每帧一个 JSON 对象 + "\n" 分隔符，grpc-gateway JSONPb.Delimiter）。
// 解析器独立成纯函数形态（可单测），按 chunk 推进、断帧自动拼接。

import { handleUnauthorized } from "./client";
import type { ErrorEnvelope } from "./errors";

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
 * 读空闲看门狗超时（M9-2）：N 秒无帧即 abort + onEnd（走调用方的退避
 * 重连路径）。半开连接（TCP 未断但对端已死）下 reader.read() 永不
 * settle，没有该看门狗则流永久挂起且退避机制失效。导出常量供测试
 * 以 fake timers 推进。
 */
export const STREAM_IDLE_TIMEOUT_MS = 30_000;

function newIdleTimeoutError(): StreamError {
  return new StreamError(0, {
    code: "E_STREAM_IDLE_TIMEOUT",
    message: `no frames received for ${STREAM_IDLE_TIMEOUT_MS}ms — closing half-open connection`,
  });
}

/**
 * 打开一条 NDJSON 流（fetch + ReadableStream）。
 * - headers 由调用方提供 Bearer；鉴权失败（401）在 consume 内走
 *   client.ts 的统一 401 处置（清凭据 + App 级未授权监听 → 回登录页），
 *   再以 StreamError 上抛——页面只做展示，不再各自拦 401。
 * - 返回的 connection 可主动关闭（AbortController）。
 * - 读空闲看门狗：STREAM_IDLE_TIMEOUT_MS 内无新 chunk 即 abort 并以
 *   StreamError(E_STREAM_IDLE_TIMEOUT) 触发 onEnd（区别于主动关闭的
 *   静默返回）。
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
  // 看门狗触发标记（外层 catch 需读——区分主动关闭与超时断流）。
  let idleFired = false;
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
      // 流式面 401 与 api() 同源处置：全局登出在此统一触发（幂等），页面
      // 的 401 展示分支不会重复处置。
      if (response.status === 401) {
        handleUnauthorized(envelope as ErrorEnvelope);
      }
      throw newStreamError(response.status, envelope);
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    // 看门狗状态：每次收到 chunk 重置；触发时置位 idleFired。
    let idleTimer: ReturnType<typeof setTimeout> | undefined;
    let idleRace: Promise<never> | null = null;
    const clearIdleWatchdog = () => {
      if (idleTimer !== undefined) clearTimeout(idleTimer);
      idleTimer = undefined;
      idleRace = null;
    };
    const armIdleWatchdog = () => {
      clearIdleWatchdog();
      idleRace = new Promise<never>((_, reject) => {
        idleTimer = setTimeout(() => {
          idleFired = true;
          // 拆底层连接（真实 fetch body 会随之 error）；半开形态下即便
          // read 不响应 abort，race 兜底保证 onEnd 必达。
          controller.abort();
          reject(newIdleTimeoutError());
        }, STREAM_IDLE_TIMEOUT_MS);
      });
    };
    try {
      for (;;) {
        armIdleWatchdog();
        const readP = reader.read();
        // race 落败方（abort 后的 AbortError）迟到 reject 不至于变成
        // unhandled rejection。
        readP.catch(() => undefined);
        const { done, value } = await Promise.race([readP, idleRace!]);
        clearIdleWatchdog();
        if (done) break;
        for (const frame of parser.push(decoder.decode(value, { stream: true }))) {
          handlers.onFrame(frame);
        }
      }
    } finally {
      clearIdleWatchdog();
    }
    for (const frame of parser.flush()) {
      handlers.onFrame(frame);
    }
    handlers.onEnd?.();
  } catch (err) {
    // 主动关闭：非异常（看门狗触发的 abort 除外——那是需要重连的断流）。
    if (controller.signal.aborted && !idleFired) return;
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
