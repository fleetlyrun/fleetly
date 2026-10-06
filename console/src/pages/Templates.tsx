import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { apiFetch, apiSend } from "../api/client";
import type { components } from "../api/templates";
import { ErrorNote, EmptyNote, Field, LoadingNote, Modal, PageShell, PrimaryButton, Select, TableHead, TableWrap, TextInput, formatTime } from "../components/ui";
import { buildTemplateValues, inputKindOf, type TemplateVariableDecl } from "../lib/templateForm";
import { useProjects } from "../lib/catalog";

// Templates 页（F3.3，ADR-0050 决策 6）：目录列表 → 详情（变量声明 +
// compose 体只读）→ 实例化向导（project/app/values——secret 型显示
// platform-generated）→ 提交后跳部署详情 wait 流既有面。

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

interface InstantiatedResource {
  kind: string;
  id: string;
  name: string;
  reused: boolean;
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

export function TemplatesPage({ navigate }: { navigate: (path: string) => void }) {
  const catalog = useTemplatesCatalog();
  const projects = useProjects();
  const [detailName, setDetailName] = useState<string | null>(null);
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
      return {
        name: t.name,
        version: t.version ?? "",
        digest: t.digest ?? "",
        description: t.description ?? "",
        body: res.body ?? "",
        variables,
      };
    },
  });

  const [wizard, setWizard] = useState<TemplateEntry | null>(null);

  if (catalog.isPending) return <LoadingNote label="loading the template catalog" />;
  if (catalog.isError) return <ErrorNote error={catalog.error} />;

  const { source, templates } = catalog.data;
  return (
    <PageShell
      title={`Templates — catalog source: ${source}`}
      hint="secret variables are platform-generated and injected as files; values are never shown"
    >
      {templates.length === 0 ? (
        <EmptyNote label="the catalog is empty" />
      ) : (
        <TableWrap>
          <TableHead columns={["Name", "Version", "Description", ""]} />
          <tbody>
            {templates.map((t) => (
              <tr key={t.name} className="border-t border-neutral-200 dark:border-neutral-800">
                <td className="p-2 font-mono">{t.name}</td>
                <td className="p-2">{t.version}</td>
                <td className="p-2">{t.description}</td>
                <td className="p-2 text-right whitespace-nowrap">
                  <button className="text-blue-600 hover:underline" onClick={() => setDetailName(t.name)}>
                    show
                  </button>
                  <span className="mx-2 text-neutral-400">·</span>
                  <button className="text-blue-600 hover:underline" onClick={() => setWizard(t)}>
                    instantiate
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </TableWrap>
      )}

      <Modal title={detailName ? `Template ${detailName}` : ""} open={detailName !== null} onClose={() => setDetailName(null)}>
        {detailQuery.isPending && <LoadingNote label="loading the template" />}
        {detailQuery.isError && <ErrorNote error={detailQuery.error} />}
        {detailQuery.data && (
          <div className="space-y-3">
            <p className="text-sm text-neutral-600 dark:text-neutral-400">
              {detailQuery.data.description} · version {detailQuery.data.version} · {detailQuery.data.digest.slice(0, 19)}…
            </p>
            {detailQuery.data.variables.length > 0 && (
              <div>
                <p className="font-semibold mb-1">Variables</p>
                <ul className="text-sm space-y-1">
                  {detailQuery.data.variables.map((v) => (
                    <li key={v.name}>
                      <span className="font-mono">{v.name}</span> ({v.type}
                      {v.required ? ", required" : ""}
                      {v.default ? `, default ${v.default}` : ""}) — {v.description}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            <div>
              <p className="font-semibold mb-1">Template body</p>
              <pre className="text-xs bg-neutral-100 dark:bg-neutral-900 rounded p-2 overflow-x-auto">{detailQuery.data.body}</pre>
            </div>
          </div>
        )}
      </Modal>

      {wizard && (
        <InstantiateWizard
          template={wizard}
          projects={projects.data ?? []}
          onClose={() => setWizard(null)}
          onDeployed={(deploymentId) => {
            setWizard(null);
            navigate(`deployments/${deploymentId}`);
          }}
        />
      )}
    </PageShell>
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
      const res = await apiSend<{ app_id?: string; deployment_id?: string; resources?: Array<InstantiatedResource | undefined> }>(
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
    <Modal title={`Instantiate ${template.name}`} open onClose={onClose}>
      <form className="space-y-3" onSubmit={submit}>
        <Field label="Project">
          <Select value={projectId} onChange={(e) => setProjectId(e.target.value)} required>
            <option value="">select a project…</option>
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="App name" hint="an existing app with this name is reused — re-running re-deploys">
          <TextInput value={appName} onChange={(e) => setAppName(e.target.value)} required />
        </Field>
        {inputVars.map((v) => (
          <Field key={v.name} label={`${v.name} (${v.type}${v.required ? ", required" : ""})`} hint={v.description}>
            <TextInput
              value={values[v.name] ?? ""}
              onChange={(e) => setValues((prev) => ({ ...prev, [v.name]: e.target.value }))}
              placeholder={v.default || (v.type === "domain" ? "host serving this app" : "")}
            />
          </Field>
        ))}
        {template.variables.some((v) => v.type === "secret") && (
          <p className="text-xs text-neutral-600 dark:text-neutral-400">
            secret variables ({template.variables.filter((v) => v.type === "secret").map((v) => v.name).join(", ")}) are
            platform-generated and injected as files; their values are never shown.
          </p>
        )}
        {error !== null && <ErrorNote error={error} />}
        <div className="flex justify-end gap-2">
          <PrimaryButton type="submit" disabled={pending || projectId === ""}>
            {pending ? "instantiating…" : "instantiate"}
          </PrimaryButton>
        </div>
        <p className="text-xs text-neutral-500">submitted at {formatTime(new Date().toISOString())} — the deployment page streams states to terminal</p>
      </form>
    </Modal>
  );
}
