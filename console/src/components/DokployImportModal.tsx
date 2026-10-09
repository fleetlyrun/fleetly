import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { apiFetch, apiSend } from "../api/client";
import { parseDokploy, type DokployPlan } from "../lib/dokploy";
import { Modal, Field, TextInput, TextArea, PrimaryButton, ErrorNote, TableWrap, TableHead } from "./ui";

// DokployImportModal 是竞品迁移钩子的 Console 面（C3）：解析（TS 移植
// 解析器，parity 由 dokploy.test.ts 钉死）→ 计划预览（apps/databases/
// skipped 全量诚实呈现）→ 执行（create-or-reuse 编排，与 CLI
// create-from-dokploy 同序：project → databases → routes 全量表 →
// 逐 app（ensure → deploy → routes））。数据面不搬移——库内容走
// Backup/Restore，执行完的注记与 CLI 口径一致。

interface Step {
  kind: string;
  name: string;
  detail: string;
  reused: boolean;
}

export function DokployImportModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [projectName, setProjectName] = useState("");
  const [raw, setRaw] = useState("");
  const [plan, setPlan] = useState<DokployPlan | null>(null);
  const [parseError, setParseError] = useState<unknown>(null);
  const [steps, setSteps] = useState<Step[]>([]);
  const [running, setRunning] = useState(false);
  const [runError, setRunError] = useState<unknown>(null);

  function parse() {
    setParseError(null);
    setPlan(null);
    setSteps([]);
    setRunError(null);
    try {
      setPlan(parseDokploy(raw));
    } catch (cause) {
      setParseError(cause);
    }
  }

  async function execute() {
    if (plan == null) return;
    setRunning(true);
    setRunError(null);
    const log: Step[] = [];
    const step = (s: Step) => {
      log.push(s);
      setSteps([...log]);
    };
    try {
      // project create-or-reuse（CLI ensureProjectByName 同款幂等链）。
      let projectId = "";
      const projects = await apiFetch<{ projects?: Array<{ id?: string; name?: string } | undefined> }>("/v1/projects?limit=200");
      for (const p of projects.projects ?? []) {
        if (p != null && p.name === projectName && p.id != null) {
          projectId = p.id;
          break;
        }
      }
      if (projectId === "") {
        const created = await apiSend<{ project?: { id?: string } }>("/v1/projects", "POST", { name: projectName });
        projectId = created.project?.id ?? "";
        step({ kind: "project", name: projectName, detail: projectId, reused: false });
      } else {
        step({ kind: "project", name: projectName, detail: projectId, reused: true });
      }

      // databases create-or-reuse（按名——重跑收敛）。
      const dbs = await apiFetch<{ databases?: Array<{ id?: string; name?: string } | undefined> }>(`/v1/databases?project_id=${encodeURIComponent(projectId)}`);
      for (const db of plan.databases) {
        const existing = (dbs.databases ?? []).find((row) => row != null && row.name === db.name);
        if (existing?.id != null) {
          step({ kind: "database", name: db.name, detail: existing.id, reused: true });
          continue;
        }
        const created = await apiSend<{ database?: { id?: string } }>("/v1/databases", "POST", { project_id: projectId, name: db.name, engine: db.engine });
        step({ kind: "database", name: db.name, detail: created.database?.id ?? "", reused: false });
      }

      // routes 全量拉取一次（host 复用判定——host 全局唯一）。
      const routeRows = await apiFetch<{ routes?: Array<{ id?: string; host?: string } | undefined> }>("/v1/routes?limit=200");
      const routeByHost = new Map<string, string>();
      for (const r of routeRows.routes ?? []) {
        if (r != null && r.host != null && r.id != null) routeByHost.set(r.host, r.id);
      }

      for (const app of plan.apps) {
        // app create-or-reuse。
        const apps = await apiFetch<{ apps?: Array<{ id?: string; name?: string } | undefined> }>(`/v1/apps?project_id=${encodeURIComponent(projectId)}`);
        let appId = "";
        let reused = false;
        for (const row of apps.apps ?? []) {
          if (row != null && row.name === app.name && row.id != null) {
            appId = row.id;
            reused = true;
            break;
          }
        }
        if (appId === "") {
          const created = await apiSend<{ app?: { id?: string } }>("/v1/apps", "POST", { project_id: projectId, name: app.name });
          appId = created.app?.id ?? "";
        }
        step({ kind: "app", name: app.name, detail: appId, reused });

        // deploy（image 直投带 env / compose 文本——env 已在解析期插值，
        // 与 DeployRequest.env 互斥，CLI 同款）。
        const body =
          app.composeYaml !== ""
            ? { app_id: appId, compose_yaml: app.composeYaml }
            : { app_id: appId, image: app.image, env: Object.keys(app.env).length > 0 ? app.env : undefined };
        const dep = await apiSend<{ deployment?: { id?: string } }>("/v1/deployments", "POST", body);
        step({ kind: "deployment", name: app.name, detail: dep.deployment?.id ?? "", reused: false });

        for (const route of app.routes) {
          if (routeByHost.has(route.host)) {
            step({ kind: "route", name: route.host, detail: routeByHost.get(route.host) ?? "", reused: true });
            continue;
          }
          const created = await apiSend<{ route?: { id?: string } }>("/v1/routes", "POST", {
            project_id: projectId,
            host: route.host,
            app_id: appId,
            process: route.process,
            port: route.port,
            protocol: "http",
          });
          const id = created.route?.id ?? "";
          routeByHost.set(route.host, id);
          step({ kind: "route", name: route.host, detail: id, reused: false });
        }
      }
      void queryClient.invalidateQueries({ queryKey: ["catalog"] });
      void queryClient.invalidateQueries({ queryKey: ["resources"] });
    } catch (cause) {
      setRunError(cause);
    } finally {
      setRunning(false);
    }
  }

  const done = steps.length > 0 && !running && runError == null;
  return (
    <Modal title="Import from dokploy" open={open} onClose={onClose}>
      {steps.length === 0 ? (
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            parse();
          }}
        >
          <Field label="Target project" hint="reused if it exists, created otherwise">
            <TextInput value={projectName} onChange={(event) => setProjectName(event.target.value)} placeholder="migrated-from-dokploy" autoFocus />
          </Field>
          <Field label="Dokploy export JSON" hint="applications / compose / domains / databases arrays per the dokploy API model">
            <TextArea value={raw} onChange={(event) => setRaw(event.target.value)} rows={10} placeholder="{ &quot;applications&quot;: [...], … }" spellCheck={false} className="font-mono text-xs" />
          </Field>
          {parseError != null ? <ErrorNote error={parseError} hint="The export could not be parsed as JSON." /> : null}
          <div className="flex justify-end gap-2">
            <PrimaryButton type="button" disabled={projectName === "" || raw.trim() === ""} onClick={parse}>
              Parse plan
            </PrimaryButton>
          </div>
        </form>
      ) : null}

      {plan != null && steps.length === 0 ? (
        <div className="flex flex-col gap-3">
          <div className="text-xs text-slate-400">
            {plan.apps.length} app(s), {plan.databases.length} database(s), {plan.skipped.length} skipped.
          </div>
          {plan.apps.map((app) => (
            <div key={app.name} className="rounded-md border border-slate-800 px-3 py-2 text-xs">
              <div className="font-medium text-slate-200">
                app {app.name} <span className="text-slate-500">{app.composeYaml !== "" ? "(compose)" : `image ${app.image}`}</span>
              </div>
              {app.routes.map((route) => (
                <div key={`${route.host}:${route.port}`} className="ml-3 font-mono text-slate-500">
                  route {route.host} → {route.process}:{route.port}
                </div>
              ))}
            </div>
          ))}
          {plan.databases.map((db) => (
            <div key={db.name} className="rounded-md border border-slate-800 px-3 py-2 text-xs text-slate-300">
              database {db.name} <span className="text-slate-500">(engine {db.engine})</span>
            </div>
          ))}
          {plan.skipped.length > 0 ? (
            <div className="flex flex-col gap-2 rounded-md border border-amber-900/60 bg-amber-950/30 px-3 py-2">
              {plan.skipped.map((skip, index) => (
                <div key={index} className="text-xs text-amber-200/80">
                  skipped {skip.kind} “{skip.item}”: {skip.reason}
                </div>
              ))}
            </div>
          ) : null}
          {runError != null ? <ErrorNote error={runError} hint="The migration stopped at the failed step — fix and rerun; create-or-reuse steps converge." /> : null}
          <div className="flex justify-end gap-2">
            <PrimaryButton
              type="button"
              disabled={running || plan.apps.length + plan.databases.length === 0}
              onClick={() => void execute()}
            >
              {running ? "Migrating…" : "Execute migration"}
            </PrimaryButton>
          </div>
        </div>
      ) : null}

      {steps.length > 0 ? (
        <div className="flex flex-col gap-3">
          <TableWrap>
            <table className="w-full text-left text-sm">
              <TableHead columns={["kind", "name", "detail", ""]} />
              <tbody>
                {steps.map((s, index) => (
                  <tr key={index} className="border-t border-slate-800">
                    <td className="px-3 py-1.5 text-xs text-slate-300">{s.kind}</td>
                    <td className="px-3 py-1.5 text-xs text-slate-200">{s.name}</td>
                    <td className="px-3 py-1.5 font-mono text-xs text-slate-500">{s.detail || "—"}</td>
                    <td className="px-3 py-1.5 text-xs">{s.reused ? <span className="text-slate-500">reused</span> : <span className="text-emerald-300">created</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          {runError != null ? (
            <ErrorNote error={runError} hint="The migration stopped at the failed step — fix and rerun; create-or-reuse steps converge." />
          ) : null}
          {done && plan != null && plan.databases.length > 0 ? (
            <div className="rounded-md border border-slate-800 px-3 py-2 text-xs text-slate-400">
              Note: database data was not moved; it still lives on the dokploy host — restore it via Backup/Restore or a manual export/reload.
            </div>
          ) : null}
          <div className="flex justify-end">
            <PrimaryButton type="button" disabled={running} onClick={onClose}>
              {done ? "Close" : "Running…"}
            </PrimaryButton>
          </div>
        </div>
      ) : null}
    </Modal>
  );
}
