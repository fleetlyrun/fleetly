import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { AppSpecTab } from "@/features/apps-tabs/app-spec";

// App 详情 · Spec tab（ADR-0071 配置可观测面）：冻结 Spec 全文 / Workload
// 投影 / 在服观测三段式；seq search 参数承接部署详情的 View spec 链接。
const specSearch = z.object({ seq: z.number().int().positive().optional() });

export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/spec")({
  validateSearch: specSearch,
  component: AppSpecRoute,
});

function AppSpecRoute() {
  const { projectId, appId } = Route.useParams();
  const { seq } = Route.useSearch();
  return <AppSpecTab projectId={projectId} appId={appId} seq={seq} />;
}
