import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useCollection, useRecord } from "@lynx-go/uikit-core";
import { CircleCheckIcon } from "lucide-react";
import { DeploySheet } from "@/features/deployments/deploy-sheet";
import { FindingCard, UnreadableBanner } from "@/features/findings/finding-card";
import { appsResource, appsDirectorySearch } from "@/kit/apps";
import { appDiagnosisResource } from "@/kit/diagnosis";
import { describeApiError } from "@/kit/errors";

// App 详情 · Diagnosis tab（B2a W4，设计 §2.5 四层形态）：DiagnoseApp 读面
// （kit record，15s 拍子）→ 条目卡列 + unreadable 横幅（读不了 ≠ 不存在，
// 防「安静 = 健康」误读）+ 空态 = 健康绿。纯读页：无命令、无铸票——fix
// 是深链（Processes 页动作钮 / DeploySheet 部署面）不是本页直发（写面零
// 触碰）。挂位对照 Processes/Overview：features/apps-tabs 宿主 + 薄壳路由
// 只挂参。
export function AppDiagnosisTab({ projectId, appId, routeId }: { projectId: string; appId: string; routeId: string }) {
  const navigate = useNavigate();
  const diagnosis = useRecord(appDiagnosisResource, {
    id: "current",
    scope: { projectId, appId },
    query: { refetchInterval: 15_000, enabled: appId !== "" },
  });
  // 目录伴读（DeploySheet 的 App 选择面；与 hero/Processes 同键去重）。
  const apps = useCollection(appsResource, {
    from: routeId,
    search: appsDirectorySearch,
    scope: { projectId },
    query: { refetchInterval: 60_000 },
  });
  // 无 verb 条目的部署面入口：声明改动走 DeploySheet（B1 预览步在内——
  // 「确认的正是展示的」，诊断面不提供绕过预览的直发通道）。
  const [deployOpen, setDeployOpen] = useState(false);
  const appBase = `/p/${encodeURIComponent(projectId)}/apps/${encodeURIComponent(appId)}`;
  const findings = diagnosis.data?.findings ?? [];
  const unreadableFaces = diagnosis.data?.unreadableFaces ?? [];

  if (diagnosis.status === "loading") {
    return <p className="py-10 text-center text-xs text-muted">Running diagnosis…</p>;
  }
  if (diagnosis.status === "error") {
    return <p className="py-10 text-center text-xs text-err">{describeApiError(diagnosis.error ?? { kind: "network" })}</p>;
  }
  return (
    <div className="flex flex-col gap-4">
      <UnreadableBanner faces={unreadableFaces} />
      {findings.length === 0 ? (
        // 空态 = 健康（绿）：诊断只回答「为什么现在坏」——无条目 = 规则可
        // 读的面内无失败态（unreadable 横幅在场时这层健康是部分的，横幅
        // 已如实交代）。
        <div className="rounded-xl border border-ok/30 bg-ok/10 p-6 text-center">
          <CircleCheckIcon className="mx-auto size-5 text-ok" />
          <h3 className="mt-2 text-sm font-semibold text-ok">No findings — nothing to fix right now</h3>
          <p className="mt-1 text-xs text-muted">
            Diagnosis explains why something is failing; on the faces it could read, this app has no failure state. Deployment
            failures live on the Deployments tab with their build logs.
          </p>
        </div>
      ) : (
        <>
          <p className="text-xs text-muted">
            {findings.length} finding{findings.length === 1 ? "" : "s"} — grouped per workload, most severe first as the rules
            order them.
          </p>
          <div className="flex flex-col gap-3">
            {findings.map((finding) => (
              <FindingCard
                key={`${finding.finding_id}-${finding.workload_id}`}
                finding={finding}
                appBase={appBase}
                onOpenDeploy={() => setDeployOpen(true)}
              />
            ))}
          </div>
        </>
      )}
      <DeploySheet
        open={deployOpen}
        onOpenChange={setDeployOpen}
        apps={apps.rows}
        defaultAppId={appId}
        projectId={projectId}
        from={routeId}
        onDeployed={(deploymentId) => {
          // 受理成功落部署详情：typed 路由 SPA 导航（Processes 同款锚）。
          void navigate({
            to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
            params: { projectId, appId, deploymentId },
          });
        }}
      />
    </div>
  );
}
