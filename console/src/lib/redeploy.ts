import { apiSend } from "@/api/client";
import { fetchFrozenSpec } from "@/lib/spec-deploy";

// redeployFrozenSpec 重发 App 当前冻结 Spec（ADR-0069 批，hero Redeploy
// 数据链）：回读 GetAppSpec → 原样走 spec_file 第四源——变量三级合成
//（含 AppVariable 顶层）拾取的正道（ADR-0072：变量轮换 = 改 store +
// Redeploy）。语义如实披露（ADR-0064 内容寻址 Ensure）：内容未变的进程
// 零滚动，排障重启走 Process 级 RestartProcess。未部署 = 无冻结体，精确
// 报错（hero 侧按钮按 latest 部署禁用，此处是深度防御）。
export async function redeployFrozenSpec(appId: string): Promise<string | undefined> {
  const spec = await fetchFrozenSpec(appId);
  if (!spec) throw new Error("the app has no frozen spec yet — deploy first");
  const out = await apiSend<{ deployment?: { id?: string } }>("/v1/deployments", "POST", {
    app_id: appId,
    spec_file: JSON.stringify(spec),
  });
  return out?.deployment?.id;
}
