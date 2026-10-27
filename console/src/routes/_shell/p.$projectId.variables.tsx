import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { ConfigsPanel, SecretsPanel, VariablesPanel } from "@/features/resources/panels";
import { ModulePage } from "@/components/domain/module-page";
import { useQuery } from "@tanstack/react-query";
import { collectionQueryOptions } from "@lynx-go/uikit-core/react";
import { scoped } from "@lynx-go/uikit-core/kernel";
import { appsResource, appsDirectorySearch } from "@/kit/apps";
import { variablesSearch } from "@/kit/variables";

// Variables 页（原 Configuration，ADR-0059 同批重组；ADR-0060 路由对齐
// 页名 /variables；模板化收编 ModulePage）：secrets / configs / shared
// variables 三模块 tab 化（对齐批 5 复裁：一页多模块用 tab，形态参照
// App Detail 下划线式，URL param 深链）。
// useCollection 化批 3：validateSearch 合并形态收编 tab 键 + kit 集合参
// 数（toSearchSchema 不认 tab，必须显式并入）；tab 切换走函数式 search
// 保 q/分页跨 tab 持久（接受语义）。
const variablesTabSearch = z.object({ tab: z.enum(["secrets", "configs", "variables"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/variables")({
  validateSearch: (input: Record<string, unknown>) => ({
    ...variablesTabSearch.parse(input),
    ...variablesSearch.toSearchSchema()["~standard"].validate(input).value,
  }),
  component: VariablesPage,
});

function VariablesPage() {
  const { projectId } = Route.useParams();
  const navigate = Route.useNavigate();
  // 批4 集成收口：App 目录伴读迁 kit（collectionQueryOptions + scoped——
  // 表单/名字反查不进 URL）。
  const appsQuery = useQuery({
    ...collectionQueryOptions(scoped(appsResource, { projectId }), appsDirectorySearch.defaults, {
      refetchInterval: 60_000,
    }),
    enabled: projectId !== "",
  });
  const apps = appsQuery.data?.rows ?? [];
  const tab = Route.useSearch({ select: (search) => search.tab ?? "secrets" });
  return (
    <ModulePage
      title="Variables"
      description="Secrets, configs and shared variables for this project"
      tabs={[
        { value: "secrets", label: "Secrets" },
        { value: "configs", label: "Configs" },
        { value: "variables", label: "Shared variables" },
      ]}
      current={tab}
      onTabChange={(value) => void navigate({ search: (prev) => ({ ...prev, tab: value }) })}
    >
      {tab === "secrets" ? <SecretsPanel projectId={projectId} routeId={Route.id} /> : null}
      {tab === "configs" ? <ConfigsPanel projectId={projectId} routeId={Route.id} /> : null}
      {tab === "variables" ? (
        <VariablesPanel projectId={projectId} routeId={Route.id} apps={[...apps]} />
      ) : null}
    </ModulePage>
  );
}
