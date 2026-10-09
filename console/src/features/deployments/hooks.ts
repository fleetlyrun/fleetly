import { useQuery } from "@tanstack/react-query";
import type { components } from "@/api/delivery";
import { apiFetch } from "@/api/client";

// 部署域查询（UI v2 批 2）：per-App 列表（API 契约 app_id 必填，ADR-0044）
// 5s 轮询；项目级聚合 = 客户端 fan-out（有界 50 App、15s 档），这是
// "聚合是 API 面的事"在 Console 侧的诚实替代，不造私有 BFF。

type Deployment = components["schemas"]["v1Deployment"];

export type { Deployment };

const APP_POLL_MS = 5_000;
const PROJECT_POLL_MS = 15_000;
const FAN_OUT_LIMIT = 50;

interface DeploymentsResponse {
  deployments?: Array<Deployment | undefined>;
}

function rowsOf(list: Array<Deployment | undefined> | undefined): Deployment[] {
  return (list ?? []).flatMap((item) => (item?.id ? [item] : []));
}

export function useAppDeployments(appId: string) {
  return useQuery({
    queryKey: ["deployments", appId],
    enabled: appId !== "",
    queryFn: async (): Promise<Deployment[]> => {
      const res = await apiFetch<DeploymentsResponse>(`/v1/deployments?app_id=${encodeURIComponent(appId)}&limit=50`);
      return rowsOf(res.deployments);
    },
    refetchInterval: APP_POLL_MS,
  });
}

export function useProjectDeployments(appIds: string[]) {
  const bounded = appIds.slice(0, FAN_OUT_LIMIT);
  return useQuery({
    queryKey: ["deployments", "project-fanout", bounded],
    enabled: bounded.length > 0,
    queryFn: async (): Promise<Deployment[]> => {
      const responses = await Promise.all(
        bounded.map((appId) =>
          apiFetch<DeploymentsResponse>(`/v1/deployments?app_id=${encodeURIComponent(appId)}&limit=20`).catch(
            () => ({ deployments: [] as Array<Deployment | undefined> }),
          ),
        ),
      );
      const merged = responses.flatMap((res) => rowsOf(res.deployments));
      // 全项目时间线：按 updated_at 倒序收敛
      return merged.sort((a, b) => (b.updated_at ?? "").localeCompare(a.updated_at ?? ""));
    },
    refetchInterval: PROJECT_POLL_MS,
  });
}
