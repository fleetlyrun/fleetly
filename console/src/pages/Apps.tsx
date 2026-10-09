import { useState } from "react";
import { useApps, useProjects } from "../lib/catalog";
import { DeployForm } from "../components/DeployForm";
import { HookModal } from "../components/HookModal";
import {
  DangerRowButton,
  EmptyNote,
  ErrorNote,
  Field,
  LoadingNote,
  Modal,
  PageShell,
  PrimaryButton,
  RowButton,
  TableHead,
  TableWrap,
  TextInput,
  useApiMutation,
} from "../components/ui";

// Apps 页（F3.1 写面）：项目目录 + 项目内 App 管理——创建/删除项目与
// App、App 级部署入口。删除有守卫（项目有 App 拒/App 有活跃部署拒），
// 错误信封原样呈现（服务端执法面即 UX 面）。
export function AppsPage() {
  const [projectId, setProjectId] = useState("");
  const projects = useProjects();
  const apps = useApps(projectId);
  const effectiveProjectId = projectId || projects.data?.[0]?.id || "";

  const [projectModal, setProjectModal] = useState(false);
  const [newProject, setNewProject] = useState("");
  const createProject = useApiMutation<{ project?: { id?: string } }>({
    path: "/v1/projects",
    method: "POST",
    body: () => ({ name: newProject }),
    invalidate: [["catalog", "projects"]],
  });

  const [appModal, setAppModal] = useState(false);
  const [newApp, setNewApp] = useState("");
  const createApp = useApiMutation<{ app?: { id?: string } }>({
    path: "/v1/apps",
    method: "POST",
    body: () => ({ project_id: effectiveProjectId, name: newApp }),
    invalidate: [["catalog", "apps", effectiveProjectId]],
  });

  const [deployFor, setDeployFor] = useState<string | null>(null);

  return (
    <PageShell
      title="Apps"
      hint="projects and apps — creation guards are enforced server-side"
      toolbar={
        <>
          <select
            value={effectiveProjectId}
            onChange={(event) => setProjectId(event.target.value)}
            className="rounded-md border border-slate-700 bg-slate-900 px-2 py-1 text-sm text-slate-200"
          >
            {(projects.data ?? []).length === 0 ? <option value="">no projects</option> : null}
            {(projects.data ?? []).map((project) => (
              <option key={project.id} value={project.id}>
                {project.name}
              </option>
            ))}
          </select>
          <RowButton onClick={() => setProjectModal(true)}>New project…</RowButton>
          <RowButton
            disabled={effectiveProjectId === ""}
            onClick={() => {
              setNewApp("");
              setAppModal(true);
            }}
          >
            New app…
          </RowButton>
        </>
      }
    >
      <Modal title="New project" open={projectModal} onClose={() => setProjectModal(false)}>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            createProject.mutate(undefined, { onSuccess: () => setProjectModal(false) });
          }}
        >
          <Field label="Name">
            <TextInput value={newProject} onChange={(event) => setNewProject(event.target.value)} placeholder="shop" autoFocus />
          </Field>
          {createProject.isError ? <ErrorNote error={createProject.error} /> : null}
          <div className="flex justify-end">
            <PrimaryButton disabled={createProject.isPending || newProject === ""}>{createProject.isPending ? "Creating…" : "Create"}</PrimaryButton>
          </div>
        </form>
      </Modal>
      <Modal title="New app" open={appModal} onClose={() => setAppModal(false)}>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            createApp.mutate(undefined, { onSuccess: () => setAppModal(false) });
          }}
        >
          <Field label="Name" hint={`inside project ${projects.data?.find((p) => p.id === effectiveProjectId)?.name ?? ""}`}>
            <TextInput value={newApp} onChange={(event) => setNewApp(event.target.value)} placeholder="web" autoFocus />
          </Field>
          {createApp.isError ? <ErrorNote error={createApp.error} /> : null}
          <div className="flex justify-end">
            <PrimaryButton disabled={createApp.isPending || newApp === ""}>{createApp.isPending ? "Creating…" : "Create"}</PrimaryButton>
          </div>
        </form>
      </Modal>
      {deployFor != null ? (
        <DeployForm
          open
          onClose={() => setDeployFor(null)}
          apps={apps.data ?? []}
          defaultAppId={deployFor}
          onDeployed={(id) => {
            window.location.hash = `#/deployments/${id}`;
          }}
        />
      ) : null}

      {projects.isPending ? (
        <LoadingNote label="Loading projects…" />
      ) : projects.isError ? (
        <ErrorNote error={projects.error} hint="GET /v1/projects failed." />
      ) : (projects.data ?? []).length === 0 ? (
        <EmptyNote label="No projects yet — create one, or run the quickstart wizard." />
      ) : apps.isPending ? (
        <LoadingNote label="Loading apps…" />
      ) : apps.isError ? (
        <ErrorNote error={apps.error} hint="GET /v1/apps failed." />
      ) : (apps.data ?? []).length === 0 ? (
        <EmptyNote label="No apps in this project yet." />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["App", "ID", ""]} />
            <tbody>
              {(apps.data ?? []).map((app) => (
                <AppRow key={app.id} app={app} onDeploy={() => setDeployFor(app.id)} />
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <ProjectList projects={projects.data ?? []} selected={effectiveProjectId} onSelect={setProjectId} />
    </PageShell>
  );
}

function AppRow({
  app,
  onDeploy,
}: {
  app: { id: string; name: string };
  onDeploy: () => void;
}) {
  const del = useApiMutation({
    path: `/v1/apps/${encodeURIComponent(app.id)}`,
    method: "DELETE",
    invalidate: [["catalog", "apps"]],
  });
  const [hookOpen, setHookOpen] = useState(false);
  return (
    <>
      <tr className="border-b border-slate-800/60 hover:bg-slate-900/40">
        <td className="px-3 py-2 font-medium text-slate-200">{app.name}</td>
        <td className="px-3 py-2 font-mono text-xs text-slate-500" title={app.id}>
          {app.id}
        </td>
        <td className="px-3 py-2">
          <div className="flex items-center justify-end gap-1">
            <RowButton onClick={() => (window.location.hash = `#/deployments`)}>deployments</RowButton>
            <RowButton onClick={onDeploy}>deploy…</RowButton>
            <RowButton onClick={() => setHookOpen(true)}>hook…</RowButton>
            <DangerRowButton confirm={`Delete app ${app.name}? Routes and carriers are torn down.`} disabled={del.isPending} onClick={() => void del.mutate()}>
              delete
            </DangerRowButton>
          </div>
          {del.isError ? <ErrorNote error={del.error} /> : null}
        </td>
      </tr>
      {hookOpen ? <HookModal appId={app.id} appName={app.name} open={hookOpen} onClose={() => setHookOpen(false)} /> : null}
    </>
  );
}

// ProjectList：项目清单 + 删除（有 App 拒——受影响提示即守卫文案）。
function ProjectList({
  projects,
  selected,
  onSelect,
}: {
  projects: Array<{ id: string; name: string }>;
  selected: string;
  onSelect: (id: string) => void;
}) {
  return (
    <div className="mt-6">
      <h2 className="text-sm font-semibold text-slate-300">projects</h2>
      <div className="mt-2 flex flex-wrap gap-2">
        {projects.map((project) => (
          <ProjectChip key={project.id} project={project} active={project.id === selected} onSelect={onSelect} />
        ))}
      </div>
    </div>
  );
}

function ProjectChip({
  project,
  active,
  onSelect,
}: {
  project: { id: string; name: string };
  active: boolean;
  onSelect: (id: string) => void;
}) {
  const del = useApiMutation({
    path: `/v1/projects/${encodeURIComponent(project.id)}`,
    method: "DELETE",
    invalidate: [["catalog", "projects"], ["catalog", "apps"]],
  });
  return (
    <span
      className={
        active
          ? "flex items-center gap-2 rounded-md border border-sky-800 bg-sky-950/40 px-2.5 py-1 text-xs text-sky-200"
          : "flex items-center gap-2 rounded-md border border-slate-800 px-2.5 py-1 text-xs text-slate-400"
      }
    >
      <button type="button" onClick={() => onSelect(project.id)} className="hover:text-slate-200">
        {project.name}
      </button>
      <DangerRowButton
        confirm={`Delete project ${project.name}? Refused while it still has apps.`}
        disabled={del.isPending}
        onClick={() => void del.mutate()}
        className="border-transparent px-1"
      >
        ✕
      </DangerRowButton>
    </span>
  );
}
