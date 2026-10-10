import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useRouter } from "@tanstack/react-router";
import { SparklesIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { apiFetch, apiSend } from "@/api/client";
import type { components } from "@/api/templates";
import { describeError } from "@/lib/api-errors";
import { buildTemplateValues, inputKindOf, type TemplateVariableDecl } from "@/lib/templateForm";
import { useProjects } from "@/lib/catalog";
import { EmptyState } from "@/components/domain/empty-state";
import { ErrorState } from "@/components/domain/error-state";
import { ListPage } from "@/components/domain/list-page";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";

// 模板库（F3.3/ADR-0050 决策 6 → UI v2 批 5 reskin；模板化收编 ListPage）：
// 目录 → 详情（变量声明 + compose 体只读）→ 实例化（project/app/values
// ——secret 型 platform-generated 注入文件，值不回显）→ 提交跳部署详情。
export const Route = createFileRoute("/_shell/templates")({
  component: TemplatesPageV2,
});

type Templates = components["schemas"];

interface TemplateEntry {
  name: string;
  version: string;
  digest: string;
  description: string;
  variables: TemplateVariableDecl[];
}

interface TemplateDetail extends TemplateEntry {
  body: string;
}

function useTemplatesCatalog() {
  return useQuery({
    queryKey: ["catalog", "templates"],
    queryFn: async (): Promise<{ source: string; templates: TemplateEntry[] }> => {
      const res = await apiFetch<{ source?: string; templates?: Array<Templates["v1Template"] | undefined> }>("/v1/templates");
      const rows: TemplateEntry[] = [];
      for (const t of res.templates ?? []) {
        if (!t?.name) continue;
        const variables: TemplateVariableDecl[] = [];
        for (const v of t.variables ?? []) {
          if (!v?.name) continue;
          variables.push({ name: v.name, type: v.type ?? "string", description: v.description ?? "", default: v.default ?? "", required: v.required ?? false });
        }
        rows.push({ name: t.name, version: t.version ?? "", digest: t.digest ?? "", description: t.description ?? "", variables });
      }
      return { source: res.source ?? "", templates: rows };
    },
    refetchInterval: 60_000,
  });
}

function TemplatesPageV2() {
  const navigate = useRouter();
  const catalog = useTemplatesCatalog();
  const projects = useProjects();
  const [detailName, setDetailName] = useState<string | null>(null);
  const [wizard, setWizard] = useState<TemplateEntry | null>(null);
  const detailQuery = useQuery({
    queryKey: ["catalog", "templates", detailName],
    enabled: detailName !== null,
    queryFn: async (): Promise<TemplateDetail | null> => {
      if (!detailName) return null;
      const res = await apiFetch<{ template?: Templates["v1Template"]; body?: string }>(`/v1/templates/${encodeURIComponent(detailName)}`);
      const t = res.template;
      if (!t?.name) return null;
      const variables: TemplateVariableDecl[] = [];
      for (const v of t.variables ?? []) {
        if (!v?.name) continue;
        variables.push({ name: v.name, type: v.type ?? "string", description: v.description ?? "", default: v.default ?? "", required: v.required ?? false });
      }
      return { name: t.name, version: t.version ?? "", digest: t.digest ?? "", description: t.description ?? "", body: res.body ?? "", variables };
    },
  });

  const columns: LegacyColumnDef<TemplateEntry, any>[] = [
    {
      accessorKey: "name",
      header: "Name",
      cell: ({ row }) => <span className="font-mono text-[13px] font-semibold">{row.original.name}</span>,
    },
    {
      accessorKey: "version",
      header: "Version",
      cell: ({ row }) => <span className="font-mono text-xs text-muted-foreground">{row.original.version}</span>,
    },
    {
      accessorKey: "description",
      header: "Description",
      cell: ({ row }) => (
        <span className="block max-w-96 truncate text-[13px] text-muted-foreground" title={row.original.description}>
          {row.original.description}
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => (
        <div className="whitespace-nowrap text-right">
          <Button variant="ghost" size="sm" onClick={() => setDetailName(row.original.name)}>
            Show
          </Button>
          <Button size="sm" className="ml-1.5" onClick={() => setWizard(row.original)}>
            Instantiate…
          </Button>
        </div>
      ),
    },
  ];

  return (
    <ListPage
      title="Templates"
      description={`One-click deploys from the catalog — source: ${catalog.data?.source ?? "…"}. Secret variables are platform-generated and injected as files; values are never shown.`}
      label="templates"
      placeholder="Filter templates..."
      rows={catalog.data?.templates ?? []}
      fieldsOf={(template) => [template.name, template.description]}
      loading={catalog.isPending}
      error={catalog.isError ? catalog.error : null}
      onRetry={() => void catalog.refetch()}
      columns={columns}
      empty={
        <EmptyState icon={SparklesIcon} title="The catalog is empty" description="Refresh the catalog source or add templates to the registry." />
      }
      appendix={
        <>
          {/* 详情：变量声明 + compose 体只读 */}
          <Dialog open={detailName !== null} onOpenChange={(open) => !open && setDetailName(null)}>
            <DialogContent className="sm:max-w-2xl">
              <DialogHeader>
                <DialogTitle>Template {detailName}</DialogTitle>
                <DialogDescription>
                  {detailQuery.data
                    ? `${detailQuery.data.description} · version ${detailQuery.data.version} · ${detailQuery.data.digest.slice(0, 19)}…`
                    : "loading…"}
                </DialogDescription>
              </DialogHeader>
              {detailQuery.isPending ? <Skeleton className="h-40 w-full" /> : null}
              {detailQuery.isError ? <ErrorState error={detailQuery.error} onRetry={() => void detailQuery.refetch()} /> : null}
              {detailQuery.data ? (
                <div className="flex flex-col gap-3">
                  {detailQuery.data.variables.length > 0 ? (
                    <div>
                      <p className="mb-1 text-xs font-semibold">Variables</p>
                      <ul className="space-y-1 text-[13px]">
                        {detailQuery.data.variables.map((v) => (
                          <li key={v.name}>
                            <span className="font-mono">{v.name}</span> ({v.type}
                            {v.required ? ", required" : ""}
                            {v.default ? `, default ${v.default}` : ""}) — {v.description}
                          </li>
                        ))}
                      </ul>
                    </div>
                  ) : null}
                  <div>
                    <p className="mb-1 text-xs font-semibold">Template body</p>
                    <pre className="max-h-64 overflow-auto rounded-lg border bg-muted/40 p-3 font-mono text-xs">{detailQuery.data.body}</pre>
                  </div>
                </div>
              ) : null}
            </DialogContent>
          </Dialog>

          {wizard ? (
            <InstantiateWizard
              template={wizard}
              projects={projects.data ?? []}
              onClose={() => setWizard(null)}
              onDeployed={(deploymentId) => {
                setWizard(null);
                navigate.history.push(`/deployments/${encodeURIComponent(deploymentId)}`);
              }}
            />
          ) : null}
        </>
      }
    />
  );
}

function InstantiateWizard({
  template,
  projects,
  onClose,
  onDeployed,
}: {
  template: TemplateEntry;
  projects: Array<{ id: string; name: string }>;
  onClose: () => void;
  onDeployed: (deploymentId: string) => void;
}) {
  const [projectId, setProjectId] = useState(projects[0]?.id ?? "");
  const [appName, setAppName] = useState(template.name);
  const [values, setValues] = useState<Record<string, string>>({});
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const inputVars = template.variables.filter((v) => inputKindOf(v) === "text");

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setPending(true);
    setError(null);
    try {
      const res = await apiSend<{ app_id?: string; deployment_id?: string }>(
        "/v1/templates/instantiate",
        "POST",
        {
          project_id: projectId,
          template: template.name,
          app_name: appName,
          values: buildTemplateValues(template.variables, values),
        },
      );
      if (!res.deployment_id) throw new Error("the platform returned no deployment reference");
      onDeployed(res.deployment_id);
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Instantiate {template.name}</DialogTitle>
          <DialogDescription>
            Creates the app (existing name is reused — re-running re-deploys) and starts the first deployment.
          </DialogDescription>
        </DialogHeader>
        <form className="flex flex-col gap-3" onSubmit={submit}>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Project</Label>
            <Select value={projectId} onValueChange={setProjectId}>
              <SelectTrigger>
                <SelectValue placeholder="select a project…" />
              </SelectTrigger>
              <SelectContent>
                {projects.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">App name</Label>
            <Input value={appName} onChange={(event) => setAppName(event.target.value)} required />
          </div>
          {inputVars.map((v) => (
            <div key={v.name} className="flex flex-col gap-1.5">
              <Label className="text-xs">
                {v.name} ({v.type}
                {v.required ? ", required" : ""})
              </Label>
              <Input
                value={values[v.name] ?? ""}
                onChange={(event) => setValues((prev) => ({ ...prev, [v.name]: event.target.value }))}
                placeholder={v.default || (v.type === "domain" ? "host serving this app" : "")}
              />
              {v.description ? <p className="text-[11px] text-muted-foreground">{v.description}</p> : null}
            </div>
          ))}
          {template.variables.some((v) => v.type === "secret") ? (
            <p className="text-xs text-muted-foreground">
              secret variables ({template.variables.filter((v) => v.type === "secret").map((v) => v.name).join(", ")}) are
              platform-generated and injected as files; their values are never shown.
            </p>
          ) : null}
          {error !== null ? (
            <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 font-mono text-[11px] break-all text-muted-foreground">
              {describeError(error).detail}
            </div>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || projectId === ""}>
              {pending ? "Instantiating…" : "Instantiate"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
