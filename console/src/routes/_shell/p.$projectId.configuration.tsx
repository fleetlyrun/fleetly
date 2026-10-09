import { createFileRoute } from "@tanstack/react-router";
import { ConfigsPanel, SecretsPanel, VariablesPanel } from "@/features/resources/panels";
import { PageHeader } from "@/components/domain/page-header";
import { useApps } from "@/lib/catalog";

// 配置页（UI v2 批 4）：secrets / configs / shared variables——put 型
// 写面三区（write-only 语义与版本冻结文案随面板保真）。
export const Route = createFileRoute("/_shell/p/$projectId/configuration")({
  component: ConfigurationPage,
});

function ConfigurationPage() {
  const { projectId } = Route.useParams();
  const apps = useApps(projectId);
  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader title="Configuration" description="Secrets, configs and shared variables for this project" />
      <section className="flex flex-col gap-8">
        <SecretsPanel projectId={projectId} />
        <ConfigsPanel projectId={projectId} />
        <VariablesPanel projectId={projectId} apps={apps.data ?? []} />
      </section>
    </div>
  );
}
