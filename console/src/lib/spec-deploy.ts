import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ApiError, apiFetch, apiSend } from "@/api/client";
import type { components } from "@/api/structure";

// Spec 编辑-部署动线的共享件（ADR-0072 写面：console 的"改配置"恒走
// spec_file 第四源——hook 应用服务端按运行时 overlay 受理，非 hook 应用
// full-replace；三处消费面（Redeploy/Processes/Spec 编辑器）单源于此，
// 替代此前三份手写同构装配）。

export type AppSpec = components["schemas"]["v1AppSpec"];

// fetchFrozenSpec 回读最新冻结 Spec（未部署 404 → null 空态面）。
export async function fetchFrozenSpec(appId: string): Promise<AppSpec | null> {
  try {
    const res = await apiFetch<{ spec?: AppSpec }>(`/v1/apps/${encodeURIComponent(appId)}/spec`);
    return res?.spec ?? null;
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) return null;
    throw error;
  }
}

type SpecDeployResult = { deployment?: { id?: string } };

// useSpecDeploy 是"组装 + POST /v1/deployments (spec_file)"的共享
// mutation：入参是完整 AppSpec（调用方完成编辑），成功面统一失效
// app-spec/deployments/revisions 缓存；onDeployed 回调交调用方导航。
export function useSpecDeploy(appId: string, onDeployed?: (deploymentId: string) => void) {
  const queryClient = useQueryClient();
  return useMutation<SpecDeployResult, Error, AppSpec>({
    mutationFn: async (spec) =>
      apiSend<SpecDeployResult>("/v1/deployments", "POST", {
        app_id: appId,
        spec_file: JSON.stringify(spec),
      }),
    onSuccess: (resp) => {
      void queryClient.invalidateQueries({ queryKey: ["resources", "app-spec"] });
      void queryClient.invalidateQueries({ queryKey: ["deployments"] });
      void queryClient.invalidateQueries({ queryKey: ["resources", "revisions"] });
      const id = resp?.deployment?.id;
      if (id) onDeployed?.(id);
    },
  });
}
