import { useQueries } from "@tanstack/react-query";
import { ApiError, apiFetch } from "@/api/client";
import type { components } from "@/api/structure";

export type AppSpec = components["schemas"]["v1AppSpec"];

const FAN_OUT_LIMIT = 50;

// useAppSpecs 拉项目内全部 App 的冻结 Spec（GetAppSpec per-app fan-out——
// ADR-0057 客户端聚合惯例，≤50 上限同 deployments fan-out）。反查三面共
// 用：App Variables tab / Database Used-by / Storage 挂载与上传反查
//（IA v3 二期②）。未部署的 App（无冻结 Revision）映射为缺席而非错误。
export function useAppSpecs(projectId: string, apps: Array<{ id: string }>) {
  const appIds = apps.map((app) => app.id).slice(0, FAN_OUT_LIMIT);
  const queries = useQueries({
    queries: appIds.map((appId) => ({
      queryKey: ["resources", "app-spec", projectId, appId],
      queryFn: async (): Promise<AppSpec | undefined> => {
        try {
          const res = await apiFetch<{ spec?: AppSpec }>(`/v1/apps/${encodeURIComponent(appId)}/spec`);
          return res?.spec;
        } catch (error) {
          if (error instanceof ApiError && error.status === 404) return undefined;
          throw error;
        }
      },
      enabled: appIds.length > 0,
      refetchInterval: 60_000,
    })),
  });
  const specs = new Map<string, AppSpec>();
  appIds.forEach((appId, index) => {
    const spec = queries[index]?.data;
    if (spec) specs.set(appId, spec);
  });
  return { specs, pending: queries.some((query) => query.isPending) };
}

// specIndex 把 Spec 集合反查成"引用 → 引用方 App 集合"索引（secret_refs
// / volume_id / upload id 三类锚共用）。
export function specIndex<T>(specs: Map<string, AppSpec>, anchorsOf: (spec: AppSpec) => T[]): Map<T, string[]> {
  const index = new Map<T, string[]>();
  for (const [appId, spec] of specs) {
    for (const anchor of anchorsOf(spec)) {
      const list = index.get(anchor) ?? [];
      list.push(appId);
      index.set(anchor, list);
    }
  }
  return index;
}
