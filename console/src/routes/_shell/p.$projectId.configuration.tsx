import { createFileRoute } from "@tanstack/react-router";
import { ConfigsPanel, SecretsPanel, VariablesPanel } from "@/features/resources/panels";
import { PageHeader } from "@/components/domain/page-header";
import { useApps } from "@/lib/catalog";

// Variables 页（原 Configuration，ADR-0059 同批重组：侧栏并入 Data &
// Storage 组、页名与 tab 终名 Variables 成族）：secrets / configs /
// shared variables——put 型写面三区（route 保持 /configuration）。
export const Route = createFileRoute("/_shell/p/$projectId/configuration")({
  component: VariablesPage,
});

function VariablesPage() {
  const { projectId } = Route.useParams();
  const apps = useApps(projectId);
  return (
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader title="Variables" description="Secrets, configs and shared variables for this project" />
      <section className="flex flex-col gap-8">
        <SecretsPanel projectId={projectId} />
        <ConfigsPanel projectId={projectId} />
        <VariablesPanel projectId={projectId} apps={apps.data ?? []} />
      </section>
    </div>
  );
}
