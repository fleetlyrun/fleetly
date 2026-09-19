// REST 数据面唯一入口：fetch + Bearer + 错误信封解析。所有 /v1 消费必须
// 经此模块（无旁路调用）；流式（NDJSON）见 stream.ts——鉴权与 base 解析
// 同源复用。
//
// - token：v0.1 单操作员口径，localStorage 持久（authStore）；
// - base：生产态缺省相对路径 /v1（daemon 同源托管），VITE_API_BASE 覆盖
//   （如独立域名部署）；开发态经 Vite dev proxy 同源转发；
// - 401：统一触发 onUnauthorized（回登录页），调用方无需逐点处理。

import { ApiError, type ErrorEnvelope } from "./errors";

const TOKEN_KEY = "fleetly.console.token";

export type UnauthorizedListener = (envelope: ErrorEnvelope) => void;

let unauthorizedListener: UnauthorizedListener | null = null;

/** 登录页装配时注册；仅 App 级一处。 */
export function setUnauthorizedListener(l: UnauthorizedListener | null) {
  unauthorizedListener = l;
}

export function getToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? "";
  } catch {
    return "";
  }
}

export function setToken(token: string) {
  try {
    if (token) {
      localStorage.setItem(TOKEN_KEY, token);
    } else {
      localStorage.removeItem(TOKEN_KEY);
    }
  } catch {
    // localStorage 不可用（隐私模式等）：token 仅存内存，登录态本轮有效。
  }
}

export function clearToken() {
  setToken("");
}

/**
 * 401 统一处置：清凭据 + 通知 App 级未授权监听（回登录页）。api() 与
 * NDJSON 流式面（stream.ts）共用——全局登出只在此触发一次，调用方展示
 * 层的错误分支无需（也不应）重复处置（幂等：重复调用无副作用）。
 */
export function handleUnauthorized(envelope: ErrorEnvelope) {
  clearToken();
  unauthorizedListener?.(envelope);
}

/** API 基址：缺省同源 /v1；构建时可用 VITE_API_BASE 指向其他控制面。 */
export function apiBase(): string {
  const base = import.meta.env.VITE_API_BASE as string | undefined;
  return (base && base.replace(/\/+$/, "")) || "/v1";
}

/** 端点 URL 解析（流式模块复用）。 */
export function apiUrl(path: string, query?: Record<string, string>): string {
  let url = `${apiBase()}${path}`;
  if (query) {
    const qs = new URLSearchParams(query).toString();
    if (qs) url += `?${qs}`;
  }
  return url;
}

/** proto bytes 字段在 JSON 面是 base64：compose 上行用。 */
export function utf8ToBase64(s: string): string {
  const bytes = new TextEncoder().encode(s);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
}

export interface ApiRequestInit {
  method?: "GET" | "POST" | "PUT" | "DELETE";
  /** JSON 体（自动序列化）；与 rawBody 互斥 */
  json?: unknown;
  /** proto bytes 上行（base64 编码后作 JSON 体，如 Deploy.compose） */
  rawBody?: Record<string, unknown>;
  /** 请求方取消信号（与缺省 30s 超时组合，任一触发即中止） */
  signal?: AbortSignal;
}

/** 请求缺省超时：挂起请求不再无限等待（D4-⑤）。 */
const REQUEST_TIMEOUT_MS = 30_000;

/**
 * 超时 signal：AbortSignal.timeout 的兼容实现——计时走宿主 setTimeout
 * （而非引擎内部计时器），vitest fake timers 可控；fetch 已 settle 后再
 * 触发 abort 无副作用。Node 侧 unref 不阻进程退出，浏览器无此 API。
 */
function timeoutSignal(ms: number): AbortSignal {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), ms);
  (timer as { unref?: () => void }).unref?.();
  return ctrl.signal;
}

/** 解析错误体为信封：gateway 恒回 snake_case 信封（退化形态 code 缺失）。 */
async function parseEnvelope(response: Response): Promise<ErrorEnvelope> {
  try {
    const body = (await response.json()) as Partial<ErrorEnvelope>;
    if (body && typeof body === "object") return body;
  } catch {
    // 非 JSON 错误体（代理/框架层）——保留状态码语义。
  }
  return { message: `HTTP ${response.status} ${response.statusText}` };
}

export async function api<T>(path: string, init: ApiRequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {};
  const token = getToken();
  if (token) headers.Authorization = `Bearer ${token}`;

  let body: string | undefined;
  if (init.json !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(init.json);
  } else if (init.rawBody !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(init.rawBody);
  }

  // 缺省 30s 请求级超时 + 请求方取消信号组合（任一触发即中止）。无请求方
  // 信号时直接用超时 signal（AbortSignal.any 虽按规范应忽略 null/undefined
  // 成员，jsdom 实现严格拒绝——组合仅在两信号齐备时进行，行为等价）。
  const timeout = timeoutSignal(REQUEST_TIMEOUT_MS);
  const response = await fetch(apiUrl(path), {
    method: init.method ?? "GET",
    headers,
    body,
    signal: init.signal
      ? AbortSignal.any([init.signal, timeout])
      : timeout,
  });

  if (!response.ok) {
    const envelope = await parseEnvelope(response);
    if (response.status === 401) {
      // 会话失效：全局登出走统一处置（与流式面共用）。
      handleUnauthorized(envelope);
    }
    throw new ApiError(response.status, envelope);
  }
  return (await response.json()) as T;
}
