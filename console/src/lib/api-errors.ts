import { ApiError } from "@/api/client";

// 错误分类单源（UI v2 四态契约）：分状态诚实文案的唯一出处。
// 万金油令牌报错文案已被反模式守卫禁用——每个状态说人话：
// 出了什么事 + 用户现在能做什么。信封原文（code: message）永远附带
// （诚实面：不吞服务端口径）。

export interface DescribedError {
  title: string;
  hint: string;
  /** 信封原文（code: message / 网络层原话） */
  detail: string;
}

export function describeError(error: unknown): DescribedError {
  const detail =
    error instanceof ApiError
      ? `${error.code}: ${error.message}`
      : error instanceof Error
        ? error.message
        : String(error);

  if (error instanceof ApiError) {
    if (error.status === 401) {
      return { title: "Session expired", hint: "Your token is no longer valid — sign in again to continue.", detail };
    }
    if (error.status === 403) {
      return { title: "Not allowed", hint: "The current token's role does not grant this action or scope.", detail };
    }
    if (error.status === 404) {
      return { title: "Not found", hint: "This resource does not exist or has been deleted.", detail };
    }
    if (error.status >= 500) {
      return { title: "Server error", hint: "fleetlyd failed to serve the request — check the server logs.", detail };
    }
    return { title: "Request rejected", hint: "The API rejected this request — see the envelope below.", detail };
  }

  if (error instanceof TypeError && /fetch|network/i.test(error.message)) {
    return { title: "Cannot reach the API", hint: "The gateway is unreachable — check the server or your connection.", detail };
  }
  return { title: "Something went wrong", hint: "An unexpected client error occurred — retry, or check the browser console.", detail };
}
