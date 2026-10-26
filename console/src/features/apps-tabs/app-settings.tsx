import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQueryClient, useQuery } from "@tanstack/react-query";
import { apiFetch, apiSend, ApiError } from "@/api/client";
import { CopyButton } from "@/components/domain/copy-button";
import { RelativeTime } from "@/components/domain/relative-time";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toast } from "sonner";

// App Settings scoped tab（IA v3 T2，§4.1 边界：Settings = 低频配置面 +
// 生命周期与凭证）：General 元数据 / Git deploy hook（get/set/rotate，
// secret 仅铸造时回显一次）/ Danger zone 删除（级联披露）。
// Variables（spec 面）与 Build/Processes 卡按 §4.1 挂二期 proto（spec 读取
// 通路）后并入本 tab。

type GitHook = {
  app_id?: string;
  repo?: string;
  branch?: string;
  dockerfile?: string;
  watch_paths?: string[];
  token_prefix?: string;
  created_at?: string;
  updated_at?: string;
};

function fieldError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function AppSettingsTab({
  projectId,
  appId,
  appName,
  createdAt,
  onDeleted,
}: {
  projectId: string;
  appId: string;
  appName: string;
  createdAt: string | undefined;
  onDeleted: () => void;
}) {
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 md:grid-cols-2">
        <section className="rounded-xl border bg-card p-4">
          <h3 className="mb-3 text-sm font-semibold">General</h3>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
            <dt className="text-muted-foreground">name</dt>
            <dd className="font-semibold">{appName}</dd>
            <dt className="text-muted-foreground">app id</dt>
            <dd className="flex items-center gap-1 font-mono">
              {appId}
              <CopyButton value={appId} />
            </dd>
            <dt className="text-muted-foreground">project</dt>
            <dd className="font-mono">{projectId}</dd>
            <dt className="text-muted-foreground">created</dt>
            <dd>
              <RelativeTime value={createdAt} />
            </dd>
          </dl>
          <p className="mt-3 text-2xs text-muted-foreground">
            Build &amp; process settings (source, builder, replicas, health gate) land here once the app spec read path ships
            (IA v3 §8 二期) — until then configure them via deploy.
          </p>
        </section>
        <GitHookCard appId={appId} projectId={projectId} />
      </div>

      <DangerZone appId={appId} appName={appName} projectId={projectId} onDeleted={onDeleted} />
    </div>
  );
}

function useGitHook(appId: string) {
  return useQuery({
    queryKey: ["apps", "hook", appId],
    enabled: appId !== "",
    queryFn: async () => {
      try {
        const res = await apiFetch<{ hook?: GitHook }>(`/v1/apps/${encodeURIComponent(appId)}/hook`);
        return res?.hook;
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return null;
        throw error;
      }
    },
  });
}

function GitHookCard({ appId, projectId }: { appId: string; projectId: string }) {
  const queryClient = useQueryClient();
  const hook = useGitHook(appId);
  const [form, setForm] = useState({ repo: "", branch: "main", dockerfile: "", watchPaths: "" });
  const [editing, setEditing] = useState(false);
  const [revealedSecret, setRevealedSecret] = useState<string | null>(null);
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["apps", "hook", appId] });
  // 编辑入口（SetGitHook 是全量 upsert：改配置不换 Token）——表单按现值
  // 预填，保存即回展示态。
  const startEdit = () => {
    setForm({
      repo: hook.data?.repo ?? "",
      branch: hook.data?.branch ?? "main",
      dockerfile: hook.data?.dockerfile ?? "",
      watchPaths: (hook.data?.watch_paths ?? []).join(", "),
    });
    setEditing(true);
  };

  const save = useMutation({
    mutationFn: async () =>
      apiSend<{ hook?: GitHook; secret?: string }>(`/v1/apps/${encodeURIComponent(appId)}/hook`, "POST", {
        repo: form.repo,
        branch: form.branch,
        dockerfile: form.dockerfile || undefined,
        watch_paths: form.watchPaths === "" ? undefined : form.watchPaths.split(/[,\s]+/).filter((entry) => entry !== ""),
      }),
    onSuccess: (resp) => {
      invalidate();
      setEditing(false);
      toast("Git deploy hook configured");
      if (resp?.secret) setRevealedSecret(resp.secret);
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  const rotate = useMutation({
    mutationFn: async () => apiSend<{ hook?: GitHook; secret?: string }>(`/v1/apps/${encodeURIComponent(appId)}/hook/rotate`, "POST", {}),
    onSuccess: (resp) => {
      invalidate();
      toast("Hook token rotated — the previous token is revoked");
      if (resp?.secret) setRevealedSecret(resp.secret);
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  // Deploy now：拉取通道（RefreshGitHook）——不依赖 GitHub 侧 webhook 配置
  // 即可立即部署（首配验证 / App Templates 第三方模板仓的前提原语）。
  const refresh = useMutation({
    mutationFn: async () =>
      apiSend<{ deployment_id?: string; commit?: string; admission?: { outcome?: string } }>(
        `/v1/apps/${encodeURIComponent(appId)}/hook/refresh`,
        "POST",
        {},
      ),
    onSuccess: (resp) => {
      void queryClient.invalidateQueries({ queryKey: ["deployments"] });
      const suffix = resp?.admission?.outcome === "deduplicated" ? " (deduplicated — same commit already in the pipeline)" : "";
      toast(`Deployment ${resp?.deployment_id ?? "?"} queued @ ${resp?.commit?.slice(0, 7) ?? "?"}${suffix}`);
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  // Remove：解除通道（DeleteGitHook，W-8）——token 与 webhook secret 即刻
  // 失效，材料所有权归还 deploy 面。非幂等（无 hook 即 404）。
  const remove = useMutation({
    mutationFn: async () => apiSend(`/v1/apps/${encodeURIComponent(appId)}/hook`, "DELETE"),
    onSuccess: () => {
      invalidate();
      toast("Git deploy hook removed — material ownership returned to deploys");
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });

  const configured = hook.data != null;
  return (
    <section className="rounded-xl border bg-card p-4">
      <h3 className="mb-3 text-sm font-semibold">Git deploy hook</h3>
      <p className="mb-3 rounded-lg border bg-panel-2/40 px-3 py-2 text-2xs text-muted-foreground">
        Private repository?{" "}
        <Link
          to="/p/$projectId/registry"
          params={{ projectId }}
          search={{ tab: "github" }}
          className="font-medium text-info underline-offset-2 hover:underline"
        >
          Connect a GitHub App
        </Link>{" "}
        (project or global) — or put a <span className="font-mono">git:&lt;host&gt;</span> project secret for other hosts — instead
        of a token-URL: access material resolves at build time, nothing lands in revisions, and rotating needs no redeploy.
      </p>
      {hook.isPending ? (
        <p className="text-xs text-muted-foreground">Loading hook…</p>
      ) : configured && !editing ? (
        <>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
            <dt className="text-muted-foreground">repo</dt>
            <dd className="font-mono">{hook.data!.repo ?? "—"}</dd>
            <dt className="text-muted-foreground">branch</dt>
            <dd className="font-mono">{hook.data!.branch ?? "—"}</dd>
            <dt className="text-muted-foreground">dockerfile</dt>
            <dd className="font-mono">{hook.data!.dockerfile || "—"}</dd>
            <dt className="text-muted-foreground">token</dt>
            <dd className="font-mono">{hook.data!.token_prefix ?? "—"}…</dd>
            <dt className="text-muted-foreground">updated</dt>
            <dd>
              <RelativeTime value={hook.data!.updated_at} />
            </dd>
          </dl>
          <p className="mt-3 rounded-lg border border-[color-mix(in_oklch,var(--status-info)_35%,transparent)] bg-[var(--status-info-bg)] px-3 py-2 text-2xs text-[var(--status-info)]">
            While the hook exists, the material half of the spec (source, build) is git-owned: pushes refresh it and keep
            your runtime settings, and image/compose/upload deploys are refused (ADR-0072). Removing the hook returns
            material ownership to deploys.
          </p>
          <div className="mt-3 flex gap-2">
            <Button size="sm" disabled={refresh.isPending} onClick={() => refresh.mutate()}>
              {refresh.isPending ? "Pulling…" : "Deploy now"}
            </Button>
            <Button size="sm" variant="outline" onClick={startEdit}>
              Edit
            </Button>
            <Button size="sm" variant="outline" disabled={rotate.isPending} onClick={() => rotate.mutate()}>
              Rotate token…
            </Button>
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button size="sm" variant="outline" className="text-destructive" disabled={remove.isPending}>
                  Remove…
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Remove the git deploy hook?</AlertDialogTitle>
                  <AlertDialogDescription>
                    The URL token and the GitHub webhook secret are revoked immediately — pushes to the old webhook URL fail.
                    Material ownership returns to deploys (image/compose/upload deploys are accepted again). Configure a new hook
                    any time.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction
                    disabled={remove.isPending}
                    onClick={(event) => {
                      event.preventDefault();
                      remove.mutate();
                    }}
                  >
                    {remove.isPending ? "Removing…" : "Remove"}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </div>
          <p className="mt-2 text-2xs text-muted-foreground">
            <span className="font-semibold text-foreground">Deploy now</span> pulls the branch HEAD and deploys immediately — no
            webhook required (template repos, first config). A <span className="font-mono">.yml/.yaml</span> path is taken as a
            compose file (its services become processes; images deploy directly); anything else builds as a Dockerfile. For push
            triggers, paste the webhook URL (revealed at configure/rotate time) into GitHub → Webhooks; the secret doubles as the
            signing key.
          </p>
        </>
      ) : (
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            save.mutate();
          }}
        >
          {editing ? (
            <p className="rounded-lg border bg-panel-2/40 px-3 py-2 text-2xs text-muted-foreground">
              Editing the existing hook — SetGitHook is a full upsert: fields become this save&apos;s values, the token stays
              unchanged. Repo URL must keep its scheme (https).
            </p>
          ) : null}
          <div className="flex flex-col gap-2">
            <Label htmlFor="hook-repo">Repo</Label>
            <Input id="hook-repo" className="font-mono text-xs" value={form.repo} onChange={(event) => setForm({ ...form, repo: event.target.value })} placeholder="github.com/acme/web" />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="flex flex-col gap-2">
              <Label htmlFor="hook-branch">Branch</Label>
              <Input id="hook-branch" className="font-mono text-xs" value={form.branch} onChange={(event) => setForm({ ...form, branch: event.target.value })} />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="hook-dockerfile">Compose file or Dockerfile path (optional)</Label>
              <Input id="hook-dockerfile" className="font-mono text-xs" value={form.dockerfile} onChange={(event) => setForm({ ...form, dockerfile: event.target.value })} placeholder="docker/compose.yml or ./Dockerfile" />
            </div>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="hook-watch">Watch paths (optional, comma separated)</Label>
            <Input id="hook-watch" className="font-mono text-xs" value={form.watchPaths} onChange={(event) => setForm({ ...form, watchPaths: event.target.value })} placeholder="web/, libs/" />
          </div>
          <div className="flex gap-2">
            <Button type="submit" size="sm" disabled={save.isPending || form.repo === ""}>
              {save.isPending ? "Saving…" : editing ? "Save changes" : "Configure hook"}
            </Button>
            {editing ? (
              <Button type="button" size="sm" variant="outline" onClick={() => setEditing(false)}>
                Cancel
              </Button>
            ) : null}
          </div>
        </form>
      )}
      {revealedSecret != null ? (
        <div className="mt-3 rounded-md border border-warning/40 bg-warning/10 p-2.5 text-xs">
          <p className="font-semibold">Webhook URL — shown once, paste it into GitHub (repo or GitHub App → Webhooks)</p>
          <p className="mt-1 flex items-center gap-1 font-mono text-2xs">
            {`${window.location.origin}/v1/hooks/${revealedSecret}`}
            <CopyButton value={`${window.location.origin}/v1/hooks/${revealedSecret}`} />
          </p>
          <p className="mt-1 text-2xs text-muted-foreground">
            The token doubles as the webhook signing key (secret field). Rotating revokes the old URL immediately. Or skip
            webhooks entirely — Deploy now above pulls and deploys without GitHub-side configuration.
          </p>
        </div>
      ) : null}
      {hook.isError ? <p className="mt-3 text-xs text-destructive">{fieldError(hook.error)}</p> : null}
    </section>
  );
}

function DangerZone({ appId, appName, projectId, onDeleted }: { appId: string; appName: string; projectId: string; onDeleted: () => void }) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: async () => apiSend(`/v1/apps/${encodeURIComponent(appId)}`, "DELETE"),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["catalog", "apps", projectId] });
      toast(`App ${appName} deleted`);
      onDeleted();
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  return (
    <section className="rounded-xl border border-destructive/30 bg-card p-5">
      <h3 className="mb-2 text-sm font-semibold text-destructive">Danger zone</h3>
      <p className="mb-4 text-xs text-muted-foreground">
        Deleting <span className="font-semibold text-foreground">{appName}</span> removes its deployments history, routes and exec
        sessions. Databases and volumes are kept. Recent revisions stay in the registry and must be cleaned up separately.
      </p>
      <AlertDialog>
        <AlertDialogTrigger asChild>
          <Button variant="destructive" size="sm">
            Delete app…
          </Button>
        </AlertDialogTrigger>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete app {appName}?</AlertDialogTitle>
            <AlertDialogDescription>
              This permanently removes the app, its deployments history, routes and exec sessions. Databases and volumes are kept.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={remove.isPending}
              onClick={(event) => {
                event.preventDefault();
                remove.mutate();
              }}
            >
              {remove.isPending ? "Deleting…" : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}
