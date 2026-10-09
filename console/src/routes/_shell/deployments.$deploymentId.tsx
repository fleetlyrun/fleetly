import { useQuery } from "@tanstack/react-query";
import { createFileRoute, useRouter } from "@tanstack/react-router";
import { useEffect } from "react";
import { apiFetch } from "@/api/client";
import { streamDeploymentWait } from "@/api/streams";
import { ErrorState } from "@/components/domain/error-state";

// 旧部署深链迁移（UI v2 批 2）：/deployments/<id> 只拿到 id——用 wait 流
// 首帧解析 app_id，再取 App 归属解析 project_id，随后重定向到语境化详情。
// 解析失败给诚实错误态（深链资源可能已删除）。
export const Route = createFileRoute("/_shell/deployments/$deploymentId")({
  component: LegacyDeploymentRedirect,
});

function LegacyDeploymentRedirect() {
  const { deploymentId } = Route.useParams();
  const router = useRouter();

  const resolve = useQuery({
    queryKey: ["legacy-deployment-resolve", deploymentId],
    queryFn: async (): Promise<string | null> => {
      let appId = "";
      await streamDeploymentWait(deploymentId, new AbortController().signal, (frame) => {
        const candidate = (frame as { app_id?: string }).app_id ?? "";
        if (candidate !== "") appId = candidate;
      }).catch(() => undefined);
      if (appId === "") return null;
      const res = await apiFetch<{ app?: { project_id?: string } }>(`/v1/apps/${encodeURIComponent(appId)}`);
      const projectId = res.app?.project_id ?? "";
      if (projectId === "") return null;
      return `/p/${encodeURIComponent(projectId)}/apps/${encodeURIComponent(appId)}/deployments/${encodeURIComponent(deploymentId)}`;
    },
    retry: false,
    refetchInterval: false,
  });

  useEffect(() => {
    if (resolve.data) router.history.push(resolve.data);
  }, [resolve.data, router]);

  if (resolve.isError || resolve.data === null) {
    return (
      <div className="p-8">
        <ErrorState error={resolve.isError ? resolve.error : new Error("deployment not found")} />
      </div>
    );
  }
  return null;
}
