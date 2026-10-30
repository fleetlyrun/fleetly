import { authHeaders, notifyUnauthorized } from "@/api/client";
import { getToken } from "@/lib/token";

// rest 工厂族（uikit restCollection/restRecord/restFetchAll）的 fetch 注入
// 点：Bearer 头 + 401 全局闭环与 apiFetch 同管线；错误收敛归工厂内置的
// Response→kernel ApiError 单源。
// uikit 0.5.0 回流批（M4 整取面全量铺开）：fetchRows/rowsOf/qs 三帮手随
// 19 个 kit 文件换装 restFetchAll 退役——本文件自此只剩 authFetch，是
// rest 工厂族唯一的消费方管线位（kit/errors.ts 的 toApiError 收敛样板
// 同批在整取域归零，仅 deployments 的 deploymentLookup 手写 record 保留）。

export async function authFetch(url: string, init?: RequestInit): Promise<Response> {
  const res = await fetch(url, { ...init, headers: authHeaders(init?.headers) });
  if (res.status === 401 && getToken() !== "") notifyUnauthorized();
  return res;
}
