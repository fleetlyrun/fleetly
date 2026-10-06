import { getToken } from "../lib/token";

// 公共 REST 面的 fetch 封装：同源相对路径（生产 = embed 同端口；开发 =
// vite 代理到 :9081）、Bearer 头、apperr 错误信封（snake_case JSON：
// code/message）抛 ApiError。响应字段口径 = protojson（snake_case、
// int64/uint64 为字符串——类型已由生成链钉死，页内不再转换）。
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

// errorFromResponse 尽力解析统一错误信封；解析失败退回状态行文本。
async function errorFromResponse(res: Response): Promise<ApiError> {
  let code = "unknown";
  let message = `${res.status} ${res.statusText}`.trim();
  try {
    const body = (await res.json()) as { code?: string; message?: string };
    if (body.code) code = body.code;
    if (body.message) message = body.message;
  } catch {
    // 非 JSON 错误体：保留状态行口径。
  }
  return new ApiError(res.status, code, message);
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = authHeaders(init?.headers);
  const res = await fetch(path, { ...init, headers });
  if (!res.ok) throw await errorFromResponse(res);
  return (await res.json()) as T;
}

// apiSend 是写面助手：JSON 体 + 方法动词，响应同 apiFetch 口径（200 面
// 的 protojson；错误走统一信封抛 ApiError）。REST 写面全部 body:"*"，
// 携带体的 Content-Type 是 grpc-gateway 的 JSON 判定键。
export function apiSend<T>(path: string, method: string, body?: unknown): Promise<T> {
  return apiFetch<T>(path, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

// apiSendRaw 发送任意字节体（上传面的 application/x-tar——服务端只认
// 内容寻址，Content-Type 是建议不是契约，F3.5 裁决）。
export function apiSendRaw<T>(path: string, body: ArrayBuffer | Blob, contentType: string): Promise<T> {
  return apiFetch<T>(path, {
    method: "POST",
    headers: { "Content-Type": contentType },
    body,
  });
}

// authHeaders 是当前凭证的头形态（fetch 面；SSE 面走票据不带凭证头）。
export function authHeaders(init?: HeadersInit): Headers {
  const headers = new Headers(init);
  const token = getToken();
  if (token !== "") headers.set("Authorization", `Bearer ${token}`);
  return headers;
}
