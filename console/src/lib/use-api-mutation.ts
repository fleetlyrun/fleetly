import { useMutation, useQueryClient } from "@tanstack/react-query";
import { apiSend } from "@/api/client";

// useApiMutation 是写动词的统一封装（自 components/ui.tsx 收编至 lib——
// 旧共享层退役）：apiSend + 受影响查询键失效。失效粒度按资源键（目录页
// 60s 轮询兜底，写后即时反映优先）。path 可传函数（行级锚延迟求值）。
export function useApiMutation<TResponse>(options: {
  path: string | (() => string);
  method: string;
  body?: () => unknown;
  invalidate?: readonly unknown[][];
}) {
  const queryClient = useQueryClient();
  return useMutation<TResponse, Error, void>({
    mutationFn: () => apiSend<TResponse>(typeof options.path === "function" ? options.path() : options.path, options.method, options.body?.()),
    onSuccess: () => {
      for (const key of options.invalidate ?? []) void queryClient.invalidateQueries({ queryKey: [...key] });
    },
  });
}
