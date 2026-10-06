import { useState } from "react";
import { apiFetch, apiSend } from "../api/client";
import { streamDeploymentWait } from "../api/streams";
import { ErrorNote, Field, LoadingNote, PageShell, PrimaryButton, Select, TextInput, formatTime } from "../components/ui";

// Quickstart 向导（F3.1 UI 化）：CLI quickstart 的同序幂等链——ensure
// 项目（默认 quickstart）→ ensure App → ensure default 网络 → deploy（直投
// + 端口声明）→ ensure Route（sslip.io 形态缺省）→ wait 流到终态。逐步
// 日志呈现（幂等复用与新建逐条可见——与 CLI 同诚实度）。

interface StepLog {
  step: string;
  detail: string;
}

export function QuickstartPage() {
  const [form, setForm] = useState({ name: "demo", image: "nginx:1.27", port: "80", host: "", tls: "auto", project: "quickstart" });
  const [logs, setLogs] = useState<StepLog[]>([]);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [url, setUrl] = useState<string | null>(null);

  function log(step: string, detail: string) {
    setLogs((prev) => [...prev, { step, detail }]);
  }

  async function ensureNetworks(projectId: string) {
    const res = await apiFetch<{ networks?: Array<{ id?: string; name?: string } | undefined> }>(`/v1/networks?project_id=${encodeURIComponent(projectId)}`);
    const existing = (res.networks ?? []).find((network) => network?.name === "default");
    if (existing?.id) {
      log("network", `reusing default network ${existing.id.slice(0, 10)}…`);
      return;
    }
    const created = await apiSend<{ network?: { id?: string } }>("/v1/networks", "POST", { project_id: projectId, name: "default" });
    log("network", `created default network ${(created.network?.id ?? "").slice(0, 10)}…`);
  }

  async function run(event: React.FormEvent) {
    event.preventDefault();
    setRunning(true);
    setError(null);
    setLogs([]);
    setUrl(null);
    try {
      // 1. ensure project
      const projects = await apiFetch<{ projects?: Array<{ id?: string; name?: string } | undefined> }>("/v1/projects?limit=100");
      let project = (projects.projects ?? []).find((row) => row?.name === form.project);
      if (project?.id) {
        log("project", `reusing project ${form.project}`);
      } else {
        const created = await apiSend<{ project?: { id?: string } }>("/v1/projects", "POST", { name: form.project });
        project = { id: created.project?.id };
        log("project", `created project ${form.project}`);
      }
      const projectId = project.id ?? "";

      // 2. ensure app
      const apps = await apiFetch<{ apps?: Array<{ id?: string; name?: string } | undefined> }>(`/v1/apps?project_id=${encodeURIComponent(projectId)}`);
      let app = (apps.apps ?? []).find((row) => row?.name === form.name);
      if (app?.id) {
        log("app", `reusing app ${form.name}`);
      } else {
        const created = await apiSend<{ app?: { id?: string } }>("/v1/apps", "POST", { project_id: projectId, name: form.name });
        app = { id: created.app?.id };
        log("app", `created app ${form.name}`);
      }
      const appId = app.id ?? "";

      // 3. ensure default network（default 网随项目出生的形态也有——ensure 幂等）
      await ensureNetworks(projectId);

      // 4. deploy（直投 + 端口声明——quickstart 退役 compose 绕道形态，F3.5）
      const deployed = await apiSend<{ deployment?: { id?: string; state?: string } }>("/v1/deployments", "POST", {
        app_id: appId,
        image: form.image,
        port: Number(form.port),
      });
      const deploymentId = deployed.deployment?.id ?? "";
      log("deploy", `deployment ${deploymentId.slice(0, 10)}… submitted (state ${deployed.deployment?.state ?? "?"})`);

      // 5. ensure route（host 缺省 = <name>.<server-ip>.sslip.io 由服务端
      //    地址派生——Console 侧要求显式 host（无 server ip 探测面），空
      //    host 时用 <name>.127.0.0.1.sslip.io 引导（本机试用形态）。
      const host = form.host === "" ? `${form.name}.127.0.0.1.sslip.io` : form.host;
      const routes = await apiFetch<{ routes?: Array<{ id?: string; host?: string } | undefined> }>(`/v1/routes?project_id=${encodeURIComponent(projectId)}`);
      const route = (routes.routes ?? []).find((row) => row?.host === host);
      if (route?.id) {
        log("route", `reusing route ${host}`);
      } else {
        await apiSend<{ route?: { id?: string } }>("/v1/routes", "POST", {
          project_id: projectId,
          host,
          app_id: appId,
          process: "web",
          port: Number(form.port),
          protocol: "http",
          tls_mode: form.tls,
        });
        log("route", `created route ${host}`);
      }

      // 6. wait 流到终态（quickstart 的 --wait 默认形态；取消不经停）。
      const controller = new AbortController();
      const finalState = await new Promise<string>((resolve, reject) => {
        void streamDeploymentWait(deploymentId, controller.signal, (frame) => {
          const state = (frame as { state?: string }).state ?? "";
          if (["succeeded", "failed", "canceled", "superseded"].includes(state)) resolve(state);
        }).catch(reject);
      });
      log("wait", `deployment reached ${finalState} (${formatTime(new Date().toISOString())})`);
      const scheme = form.tls === "none" ? "http" : "https";
      setUrl(`${scheme}://${host}`);
      if (finalState !== "succeeded") {
        setError(new Error(`quickstart deployment ended in ${finalState}`));
      }
    } catch (cause) {
      setError(cause);
    } finally {
      setRunning(false);
    }
  }

  return (
    <PageShell title="Quickstart" hint="one form, full chain: project → app → network → deploy → route → wait">
      <form onSubmit={run} className="flex max-w-2xl flex-col gap-3 rounded-lg border border-slate-800 px-4 py-4">
        <div className="grid grid-cols-2 gap-3">
          <Field label="Project" hint="reused if it already exists">
            <TextInput value={form.project} onChange={(event) => setForm({ ...form, project: event.target.value })} />
          </Field>
          <Field label="App name" hint="reused if it already exists">
            <TextInput value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} />
          </Field>
        </div>
        <div className="grid grid-cols-3 gap-3">
          <Field label="Image">
            <TextInput value={form.image} onChange={(event) => setForm({ ...form, image: event.target.value })} />
          </Field>
          <Field label="Port">
            <TextInput value={form.port} onChange={(event) => setForm({ ...form, port: event.target.value })} inputMode="numeric" />
          </Field>
          <Field label="TLS">
            <Select value={form.tls} onChange={(event) => setForm({ ...form, tls: event.target.value })}>
              <option value="auto">auto</option>
              <option value="none">none</option>
            </Select>
          </Field>
        </div>
        <Field label="Host" hint="route hostname; default <app>.127.0.0.1.sslip.io (local trial form — use your server IP for external access)">
          <TextInput value={form.host} onChange={(event) => setForm({ ...form, host: event.target.value })} placeholder={`${form.name}.127.0.0.1.sslip.io`} />
        </Field>
        <div className="flex items-center gap-3">
          <PrimaryButton disabled={running || form.name === "" || form.image === ""}>{running ? "Running…" : "Run quickstart"}</PrimaryButton>
          {url != null ? (
            <a href={url} target="_blank" rel="noreferrer" className="text-sm font-medium text-sky-300 underline">
              {url}
            </a>
          ) : null}
        </div>
      </form>
      {error != null ? <ErrorNote error={error} /> : null}
      {logs.length > 0 ? (
        <ol className="max-w-2xl list-decimal space-y-1 rounded-lg border border-slate-800 px-6 py-3 font-mono text-xs text-slate-400">
          {logs.map((entry) => (
            <li key={`${entry.step}-${entry.detail}`}>
              <span className="text-slate-300">{entry.step}</span>: {entry.detail}
            </li>
          ))}
          {running ? <LoadingNote label="steps in progress…" /> : null}
        </ol>
      ) : null}
    </PageShell>
  );
}
