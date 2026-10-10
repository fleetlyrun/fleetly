import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { ConfigsPanel, SecretsPanel, VariablesPanel } from "@/features/resources/panels";
import { PageHeader } from "@/components/domain/page-header";
import { PageTabs } from "@/components/domain/page-tabs";
import { useApps } from "@/lib/catalog";

// Variables 页（原 Configuration，ADR-0059 同批重组；route 保持
// /configuration）：secrets / configs / shared variables 三模块 tab 化
// （对齐批 5 复裁：一页多模块用 tab，形态参照 App Detail 下划线式，
// URL param 深链）。
const variablesSearch = z.object({ tab: z.enum(["secrets", "configs", "variables"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/configuration")({
  validateSearch: variablesSearch,
  component: VariablesPage,
});

function VariablesPage() {
  const { projectId } = Route.useParams();
  const navigate = Route.useNavigate();
  const apps = useApps(projectId);
  const tab = Route.useSearch({ select: (search) => search.tab ?? "secrets" });
  return (
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader title="Variables" description="Secrets, configs and shared variables for this project" />
      <PageTabs
        tabs={[
          { value: "secrets", label: "Secrets" },
          { value: "configs", label: "Configs" },
          { value: "variables", label: "Shared variables" },
        ]}
        current={tab}
        onChange={(value) => void navigate({ search: { tab: value } })}
      />
      {tab === "secrets" ? <SecretsPanel projectId={projectId} /> : null}
      {tab === "configs" ? <ConfigsPanel projectId={projectId} /> : null}
      {tab === "variables" ? <VariablesPanel projectId={projectId} apps={apps.data ?? []} /> : null}
    </div>
  );
}
