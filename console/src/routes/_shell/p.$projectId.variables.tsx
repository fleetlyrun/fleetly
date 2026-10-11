import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { ConfigsPanel, SecretsPanel, VariablesPanel } from "@/features/resources/panels";
import { ModulePage } from "@/components/domain/module-page";
import { useApps } from "@/lib/catalog";

// Variables 页（原 Configuration，ADR-0059 同批重组；ADR-0060 路由对齐
// 页名 /variables；模板化收编 ModulePage）：secrets / configs / shared
// variables 三模块 tab 化（对齐批 5 复裁：一页多模块用 tab，形态参照
// App Detail 下划线式，URL param 深链）。
const variablesSearch = z.object({ tab: z.enum(["secrets", "configs", "variables"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/variables")({
  validateSearch: variablesSearch,
  component: VariablesPage,
});

function VariablesPage() {
  const { projectId } = Route.useParams();
  const navigate = Route.useNavigate();
  const apps = useApps(projectId);
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
      onTabChange={(value) => void navigate({ search: { tab: value } })}
    >
      {tab === "secrets" ? <SecretsPanel projectId={projectId} /> : null}
      {tab === "configs" ? <ConfigsPanel projectId={projectId} /> : null}
      {tab === "variables" ? <VariablesPanel projectId={projectId} apps={apps.data ?? []} /> : null}
    </ModulePage>
  );
}
