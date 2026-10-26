import { toast } from "sonner";
import type { ApiError, CommandResult } from "@lynx-go/uikit-core/kernel";
import { describeApiError } from "./errors";

// useCommand({onSettled}) 的 toast 单源（批2 写路径命令化配方③）：
// - ok → 调用方原句（onOk 收 payload——rollback/browse 等要从载荷取 id/url）；
// - rejected/conflict（结果通道）→ toast.error(message)——仅在**无结果盒**的面
//   （AlertDialog/ConfirmDialog 壳、行内动作）适用；有盒的面（Form 块/Sheet
//   提交面）盒子优先，不引用本帮手的结果支路，toast 不双响；
// - failure（失败通道：网络/401/5xx）→ toast.error(describeApiError)。
export function toastOnSettled<P>(onOk?: (payload: P) => void) {
  return (result: CommandResult<P> | null, failure: ApiError | null): void => {
    if (result != null) {
      if (result.status === "ok") onOk?.(result.payload);
      else toast.error(result.message);
    } else if (failure != null) {
      toast.error(describeApiError(failure));
    }
  };
}
