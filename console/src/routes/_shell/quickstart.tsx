import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { RocketIcon } from "lucide-react";
import { apiFetch, apiSend } from "@/api/client";
import { streamDeploymentWait } from "@/api/streams";
import { ErrorState } from "@/components/domain/error-state";
import { PageHeader } from "@/components/domain/page-header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

// Quickstart 向导（F3.1 UI 化 → UI v2 批 5 reskin）：CLI quickstart 的同序
// 幂等链——ensure 项目 → ensure App → ensure default 网络 → deploy（直投
// + 端口声明）→ ensure Route（sslip.io 缺省）→ wait 流到终态。逐步日志
// 呈现（幂等复用与新建逐条可见——与 CLI 同诚实度）。链路语义零改动。
export const Route = createFileRoute("/_shell/quickstart")({
  component: QuickstartPageV2,
});

interface StepLog {
  step: string;
  detail: string;
}

function QuickstartPageV2() {
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
      log("wait", `deployment reached ${finalState}`);
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
    <div className="mx-auto max-w-3xl px-6 pt-6 pb-8">
      <PageHeader title="Quickstart" description="One form, full chain: project → app → network → deploy → route → wait" />

      <form onSubmit={run} className="flex flex-col gap-3.5 rounded-xl border bg-card px-5 py-5">
        <div className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Project</Label>
            <Input value={form.project} onChange={(event) => setForm({ ...form, project: event.target.value })} />
            <p className="text-[11px] text-muted-foreground">reused if it already exists</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">App name</Label>
            <Input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} />
            <p className="text-[11px] text-muted-foreground">reused if it already exists</p>
          </div>
        </div>
        <div className="grid grid-cols-3 gap-3">
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Image</Label>
            <Input value={form.image} onChange={(event) => setForm({ ...form, image: event.target.value })} />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Port</Label>
            <Input value={form.port} onChange={(event) => setForm({ ...form, port: event.target.value })} inputMode="numeric" />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">TLS</Label>
            <Select value={form.tls} onValueChange={(value) => setForm({ ...form, tls: value })}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="auto">auto</SelectItem>
                <SelectItem value="none">none</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label className="text-xs">Host</Label>
          <Input
            value={form.host}
            onChange={(event) => setForm({ ...form, host: event.target.value })}
            placeholder={`${form.name}.127.0.0.1.sslip.io`}
          />
          <p className="text-[11px] text-muted-foreground">
            route hostname; default {"<app>"}.127.0.0.1.sslip.io (local trial form — use your server IP for external access)
          </p>
        </div>
        <div className="mt-1 flex items-center gap-3">
          <Button type="submit" disabled={running || form.name === "" || form.image === ""}>
            <RocketIcon data-icon-start-inline />
            {running ? "Running…" : "Run quickstart"}
          </Button>
          {url != null ? (
            <a href={url} target="_blank" rel="noreferrer" className="text-sm font-medium text-primary underline">
              {url}
            </a>
          ) : null}
        </div>
      </form>

      {error != null ? (
        <div className="mt-4">
          <ErrorState error={error} />
        </div>
      ) : null}

      {logs.length > 0 ? (
        <ol className="mt-4 list-decimal space-y-1 rounded-xl border bg-card px-6 py-3.5 font-mono text-xs text-muted-foreground">
          {logs.map((entry) => (
            <li key={`${entry.step}-${entry.detail}`}>
              <span className="text-foreground">{entry.step}</span>: {entry.detail}
            </li>
          ))}
          {running ? <li className="animate-pulse text-[var(--status-info)]">steps in progress…</li> : null}
        </ol>
      ) : null}
      <p className="mt-3 text-[11px] text-muted-foreground">Every step is idempotent — re-running reuses what exists.</p>
    </div>
  );
}
