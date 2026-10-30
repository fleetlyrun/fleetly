import { ApiError, apiFetch } from "@/api/client";
import type { components } from "@/api/structure";

// Spec 编辑-部署动线的读面共享件（ADR-0072 写面：console 的"改配置"恒走
// spec_file 第四源——hook 应用服务端按运行时 overlay 受理，非 hook 应用
// full-replace）。B1 W4 遗留直发面收口（2026-10-31）：写半边 useSpecDeploy
// （裸 useMutation 直发 POST /v1/deployments，在强制确认下恒收 428）退役
// ——Spec 编辑器与 Variables 的 deploy step 改走 deployments.deploy 命令
//（kit/deployments，写必经 useCommand；表单态工厂 lib/deployPayload 的
// specFileDeployState）+ ConfirmSkipDialog 双击 skip 形态。本文件只剩回读
// 半边（fetchFrozenSpec 是 redeploy 命令与 Variables 的 store 写+部署
// 组合链的共享读）。

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
