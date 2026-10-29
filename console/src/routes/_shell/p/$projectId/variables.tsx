import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { ConfigsPanel, SecretsPanel, VariablesPanel } from "@/features/resources/panels";
import { ModulePage } from "@/components/domain/module-page";
import { useCollectionData } from "@lynx-go/uikit-core";
import { appsResource } from "@/kit/apps";
import { configsSearch, secretsSearch, sharedVarsSearch } from "@/kit/variables";

// Variables 页（原 Configuration，ADR-0059 同批重组；ADR-0060 路由对齐
// 页名 /variables；模板化收编 ModulePage）：secrets / configs / shared
// variables 三模块 tab 化（对齐批 5 复裁：一页多模块用 tab，形态参照
// App Detail 下划线式，URL param 深链）。
// F1 回流批（M1+M3）：三列表各持 namespace schema（secrets.q/configs.q/
// variables.q 并存互不携带——URL 键形从共享 q 变前缀键，存量深链失效随批
// 记录）；validateSearch = tab zod + 三个 toSearchSchema().validate 输出
// 组合直挂（M3 passthrough 保 tab 键，零手写合并器）。
const variablesTabSearch = z.object({ tab: z.enum(["secrets", "configs", "variables"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/variables")({
  validateSearch: (input: Record<string, unknown>) => ({
    ...variablesTabSearch.parse(input),
    ...secretsSearch.toSearchSchema()["~standard"].validate(input).value,
    ...configsSearch.toSearchSchema()["~standard"].validate(input).value,
    ...sharedVarsSearch.toSearchSchema()["~standard"].validate(input).value,
  }),
  component: VariablesPage,
});

function VariablesPage() {
  const { projectId } = Route.useParams();
  const navigate = Route.useNavigate();
  // App 目录伴读（M1 useCollectionData——表单/名字反查不进 URL，大页窗）。
  const apps = useCollectionData(appsResource, {
    scope: { projectId },
    window: { size: 200 },
    refetchInterval: 60_000,
    enabled: projectId !== "",
  }).rows;
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
      onTabChange={(value) => void navigate({ search: (prev: Record<string, unknown>) => ({ ...prev, tab: value }) })}
    >
      {tab === "secrets" ? <SecretsPanel projectId={projectId} routeId={Route.id} /> : null}
      {tab === "configs" ? <ConfigsPanel projectId={projectId} routeId={Route.id} /> : null}
      {tab === "variables" ? (
        <VariablesPanel projectId={projectId} routeId={Route.id} apps={[...apps]} />
      ) : null}
    </ModulePage>
  );
}
