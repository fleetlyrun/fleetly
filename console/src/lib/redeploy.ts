import { ApiError, apiFetch, apiSend } from "@/api/client";

// redeployFrozenSpec 重发 App 当前冻结 Spec（ADR-0069 批，hero Redeploy
// 数据链）：回读 GetAppSpec → 原样走 spec_file 第四源——共享变量/secret
// 合成拾取的正道。语义如实披露（ADR-0064 内容寻址 Ensure）：内容未变的
// 进程零滚动，排障重启走 Process 级 RestartProcess。未部署 = 404/无冻结
// 体，精确报错（hero 侧按钮按 latest 部署禁用，此处是深度防御；404 分流
// 与 Processes 页空态同款 ApiError 形态）。
export async function redeployFrozenSpec(appId: string): Promise<string | undefined> {
  let res: { spec?: Record<string, unknown> };
  try {
    res = await apiFetch<{ spec?: Record<string, unknown> }>(`/v1/apps/${encodeURIComponent(appId)}/spec`);
  } catch (cause) {
    if (cause instanceof ApiError && cause.status === 404) {
      throw new Error("the app has no frozen spec yet — deploy first");
    }
    throw cause;
  }
  if (!res?.spec) throw new Error("the app has no frozen spec yet — deploy first");
  const out = await apiSend<{ deployment?: { id?: string } }>("/v1/deployments", "POST", {
    app_id: appId,
    spec_file: JSON.stringify(res.spec),
  });
  return out?.deployment?.id;
}
