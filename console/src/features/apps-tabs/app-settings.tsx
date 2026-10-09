import { useState } from "react";
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
          <h3 className="mb-3 text-[13px] font-semibold">General</h3>
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
          <p className="mt-3 text-[11.5px] text-muted-foreground">
            Build &amp; process settings (source, builder, replicas, health gate) land here once the app spec read path ships
            (IA v3 §8 二期) — until then configure them via deploy.
          </p>
        </section>
        <GitHookCard appId={appId} />
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

function GitHookCard({ appId }: { appId: string }) {
  const queryClient = useQueryClient();
  const hook = useGitHook(appId);
  const [form, setForm] = useState({ repo: "", branch: "main", dockerfile: "", watchPaths: "" });
  const [revealedSecret, setRevealedSecret] = useState<string | null>(null);
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["apps", "hook", appId] });

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

  const configured = hook.data != null;
  return (
    <section className="rounded-xl border bg-card p-4">
      <h3 className="mb-3 text-[13px] font-semibold">Git deploy hook</h3>
      {hook.isPending ? (
        <p className="text-xs text-muted-foreground">Loading hook…</p>
      ) : configured ? (
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
          <div className="mt-3 flex gap-2">
            <Button size="sm" variant="outline" disabled={rotate.isPending} onClick={() => rotate.mutate()}>
              Rotate token…
            </Button>
          </div>
        </>
      ) : (
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            save.mutate();
          }}
        >
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
              <Label htmlFor="hook-dockerfile">Dockerfile path (optional)</Label>
              <Input id="hook-dockerfile" className="font-mono text-xs" value={form.dockerfile} onChange={(event) => setForm({ ...form, dockerfile: event.target.value })} placeholder="./Dockerfile" />
            </div>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="hook-watch">Watch paths (optional, comma separated)</Label>
            <Input id="hook-watch" className="font-mono text-xs" value={form.watchPaths} onChange={(event) => setForm({ ...form, watchPaths: event.target.value })} placeholder="web/, libs/" />
          </div>
          <div>
            <Button type="submit" size="sm" disabled={save.isPending || form.repo === ""}>
              {save.isPending ? "Configuring…" : "Configure hook"}
            </Button>
          </div>
        </form>
      )}
      {revealedSecret != null ? (
        <div className="mt-3 rounded-md border border-warning/40 bg-warning/10 p-2.5 text-xs">
          <p className="font-semibold">Webhook secret — shown once, copy it now</p>
          <p className="mt-1 flex items-center gap-1 font-mono text-[11px]">
            {revealedSecret}
            <CopyButton value={revealedSecret} />
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
    <section className="max-w-2xl rounded-xl border border-destructive/30 bg-card p-5">
      <h3 className="mb-2 text-[13px] font-semibold text-destructive">Danger zone</h3>
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
