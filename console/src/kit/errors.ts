import { isApiError, type ApiError, type CommandResult } from "@lynx-go/uikit-core/kernel";
import { ApiError as FetchApiError } from "@/api/client";

// kit 数据面的错误映射单源（torchwood src/kit/errors.ts 同位法）：
// fetch 面（api/client.ts 的 ApiError：status/code/message，apperr 信封已
// 解析）→ kernel ApiError 封闭集。资源适配器抛出的失败必须是 kernel 形，
// 绑定层的重试分类与 states 块的错误态分诊才成立。401 到达调用方 = 全局
// 闭环已踢回登录（fetch 面在 apiFetch 内、http 管线在 onUnauthorized）。

export function toApiError(err: unknown): ApiError {
  if (isApiError(err)) return err;
  if (err instanceof FetchApiError) {
    if (err.status === 401) return { kind: "auth" };
    if (err.status === 403) return { kind: "forbidden" };
    if (err.status === 404) return { kind: "not-found" };
    return { kind: "server", status: err.status, message: `${err.code}: ${err.message}` };
  }
  if (err instanceof TypeError && /fetch|network/i.test(err.message)) {
    return { kind: "network", cause: err };
  }
  return { kind: "server", status: 0, message: err instanceof Error ? err.message : String(err) };
}

// 失败通道的用户文案（toast 用）：与 lib/api-errors.ts describeError 同口径
// 的诚实文案，kernel 形输入。结果通道（rejected/conflict）不走这里——归
// Form 块结果盒（批2 写路径命令化时接线）。
export function describeApiError(error: ApiError): string {
  switch (error.kind) {
    case "network":
      return "Cannot reach the API — check the server or your connection.";
    case "auth":
      return "Session expired — sign in again to continue.";
    case "forbidden":
      return "The current token's role does not grant this action or scope.";
    case "not-found":
      return "This resource does not exist or has been deleted.";
    case "decode":
      return `Unexpected response format (${error.detail}).`;
    case "server":
      return `Server error ${error.status}${error.message ? `: ${error.message}` : ""}`;
  }
}

// 结果两分映射（标准形，torchwood runCommandMapped 同款）：400→
// rejected{code,message}（apperr 信封 code 保留机器判别面）、409→conflict、
// 其余 throw toApiError（404 幂等与否归 descriptor.treatAs 裁决）。域有额
// 外方言（OCC 域码/字段错）时在域内自写变体复用本层。
export async function runCommandMapped<Payload>(op: () => Promise<Payload>): Promise<CommandResult<Payload>> {
  try {
    return { status: "ok", payload: await op() };
  } catch (err) {
    if (err instanceof FetchApiError) {
      if (err.status === 400) {
        return { status: "rejected", code: err.code, message: err.message };
      }
      if (err.status === 409) {
        return { status: "conflict", message: err.message };
      }
    }
    throw toApiError(err);
  }
}
