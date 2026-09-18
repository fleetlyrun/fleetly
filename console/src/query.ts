// 服务端状态统一经 react-query：缓存键约定 ["apps"] / ["app", name] /
// ["deployments", app] / ["deployment", id] / ["revisions", app] /
// ["env", app] / ["domains", app] / ["system", ...] / ["events", ...]。

import { QueryClient } from "@tanstack/react-query";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (failureCount, error) => {
        // 4xx（鉴权/校验/冲突）不重试——重试只面向传输类瞬时故障。
        const status = (error as { status?: number }).status;
        if (typeof status === "number" && status >= 400 && status < 500) {
          return false;
        }
        return failureCount < 2;
      },
      refetchOnWindowFocus: false,
    },
  },
});
