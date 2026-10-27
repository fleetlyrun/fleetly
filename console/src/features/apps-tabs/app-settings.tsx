import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useCommand } from "@lynx-go/uikit-core";
import { appTabCommands } from "@/kit/app-tabs";
import { useAppHook, useAppRevisionSpec, useAppWorkloads } from "@/kit/app-observe";
import { describeApiError } from "@/kit/errors";
import { toastOnSettled } from "@/kit/toast";
import { CopyButton } from "@/components/domain/copy-button";
import { RelativeTime } from "@/components/domain/relative-time";
import { sourceKindLabel } from "@/lib/format";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toast } from "sonner";

// App Settings scoped tab（IA v3 T2，§4.1 边界：Settings = 低频配置面 +
// 生命周期与凭证）：General 元数据 / Git deploy hook（get/set/rotate，
// secret 仅铸造时回显一次）/ Danger zone 删除（级联披露）。
// 走查反馈（2026-10-27）：部署身份的答案面入座——顶部 Deployment 摘要卡
//（source kind + 生效锚 + 按所有权一句话更新路径），General 占位注释退役
// 换真链接；未配置 hook 不再裸出创建表单（空态 + Configure 展开）。
// 写面批2 命令化（kit/app-tabs 的 hook/deleteApp 动词族，失效声明在命令内）。

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
      <DeploymentSummaryCard projectId={projectId} appId={appId} />
      <div className="grid gap-4 md:grid-cols-2">
        <section className="rounded-xl border bg-panel p-4">
          <h3 className="mb-3 text-sm font-semibold">General</h3>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
            <dt className="text-muted">name</dt>
            <dd className="font-semibold">{appName}</dd>
            <dt className="text-muted">app id</dt>
            <dd className="flex items-center gap-1 font-mono">
              {appId}
              <CopyButton value={appId} />
            </dd>
            <dt className="text-muted">project</dt>
            <dd className="font-mono">{projectId}</dd>
            <dt className="text-muted">created</dt>
            <dd>
              <RelativeTime value={createdAt} />
            </dd>
          </dl>
          <p className="mt-3 text-2xs text-muted">
            Build &amp; process settings live with the frozen spec — see the Deployment card above; edit it via{" "}
            <Link
              to="/p/$projectId/apps/$appId/spec"
              params={{ projectId, appId }}
              className="font-medium text-info underline-offset-2 hover:underline"
            >
              Spec → Edit &amp; deploy
            </Link>
            .
          </p>
        </section>
        <GitHookCard appId={appId} projectId={projectId} />
      </div>

      <DangerZone appId={appId} appName={appName} projectId={projectId} onDeleted={onDeleted} />
    </div>
  );
}

// Deployment 摘要卡（走查反馈 2026-10-27）：部署身份的答案面——source
// kind + 生效 Revision 锚 + 按材料所有权（ADR-0072）一句话更新路径，把人
// 指到正确的编辑面（Spec / Variables / Processes 或下方 hook 卡），自身
// 只读零编辑。数据全走既有 kit record 读面（workloads 生效锚 / GetRevision
// 原文判 compose / hook 所有权），零新 API。
function DeploymentSummaryCard({ projectId, appId }: { projectId: string; appId: string }) {
  const view = useAppWorkloads(projectId, appId, 0);
  const hook = useAppHook(projectId, appId);
  const seq = view.data?.revision?.seq ? Number(view.data.revision.seq) : undefined;
  // GetRevision 原文仅用于 compose 判定（sourceKindLabel 第二参）。
  const frozen = useAppRevisionSpec(projectId, appId, seq);
  const revision = view.data?.revision;

  if (view.isError) {
    return (
      <section className="rounded-xl border bg-panel p-4">
        <h3 className="mb-3 text-sm font-semibold">Deployment</h3>
        {view.error?.kind === "not-found" ? (
          <p className="text-xs text-muted">
            No effective revision yet — the spec freezes on the first deploy. Hit{" "}
            <span className="font-semibold text-fg">Deploy</span> in the header; history then lives on the{" "}
            <Link
              to="/p/$projectId/apps/$appId/deployments"
              params={{ projectId, appId }}
              className="font-medium text-info underline-offset-2 hover:underline"
            >
              Deployments
            </Link>{" "}
            tab.
          </p>
        ) : (
          <p className="text-xs text-err">{describeApiError(view.error)}</p>
        )}
      </section>
    );
  }
  if (revision == null) {
    return (
      <section className="rounded-xl border bg-panel p-4">
        <h3 className="mb-3 text-sm font-semibold">Deployment</h3>
        <p className="text-xs text-muted">Loading…</p>
      </section>
    );
  }

  const kind = sourceKindLabel(revision.source_kind, frozen.data?.revision?.source_text);
  const hookOwned = hook.data != null;
  return (
    <section className="rounded-xl border bg-panel p-4">
      <h3 className="mb-3 text-sm font-semibold">Deployment</h3>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
        <dt className="text-muted">source</dt>
        <dd className="font-mono">{kind}</dd>
        <dt className="text-muted">effective revision</dt>
        <dd className="flex items-center gap-1 font-mono">
          <Link
            to="/p/$projectId/apps/$appId/spec"
            params={{ projectId, appId }}
            className="text-info underline-offset-2 hover:underline"
          >
            R{seq}
          </Link>
          <span className="text-muted" title={revision.digest ?? ""}>
            {(revision.digest ?? "").slice(0, 12)}
          </span>
          <CopyButton value={revision.digest ?? ""} />
        </dd>
        <dt className="text-muted">updated</dt>
        <dd>
          <RelativeTime value={revision.created_at} />
        </dd>
      </dl>
      <p className="mt-3 rounded-lg border bg-panel-2/40 px-3 py-2 text-xs leading-relaxed text-muted">
        {hookOwned ? (
          <>
            <span className="font-semibold text-fg">
              Updates: push to <span className="font-mono">{hook.data?.repo}</span>
              {hook.data?.branch ? ` (${hook.data.branch})` : ""}
            </span>{" "}
            — or pull the branch HEAD any time from the git hook card below. Runtime settings stay with you: Variables and
            Processes edits deploy as a runtime overlay (ADR-0072). Full config: the{" "}
            <Link
              to="/p/$projectId/apps/$appId/spec"
              params={{ projectId, appId }}
              className="font-medium text-info underline-offset-2 hover:underline"
            >
              Spec
            </Link>{" "}
            tab.
          </>
        ) : (
          <>
            <span className="font-semibold text-fg">Updates: Deploy in the header</span> — it prefills the current spec for
            editing (same as Edit &amp; deploy on the{" "}
            <Link
              to="/p/$projectId/apps/$appId/spec"
              params={{ projectId, appId }}
              className="font-medium text-info underline-offset-2 hover:underline"
            >
              Spec
            </Link>{" "}
            tab). Environment values live in{" "}
            <Link
              to="/p/$projectId/apps/$appId/variables"
              params={{ projectId, appId }}
              className="font-medium text-info underline-offset-2 hover:underline"
            >
              Variables
            </Link>
            , replica counts on{" "}
            <Link
              to="/p/$projectId/apps/$appId/processes"
              params={{ projectId, appId }}
              className="font-medium text-info underline-offset-2 hover:underline"
            >
              Processes
            </Link>
            .
          </>
        )}
      </p>
    </section>
  );
}

// GitHookCard 未配置空态与表单态的分界（走查反馈 2026-10-27）：裸创建
// 表单的 placeholder 值形似配置实非配置——先空态说明所有权语义，
// Configure 展开后才进表单。

function useGitHook(projectId: string, appId: string) {
  // 收尾批 kit 化：hook 伴读（404 → null 未托管，kit 单源）。
  return useAppHook(projectId, appId);
}

function GitHookCard({ appId, projectId }: { appId: string; projectId: string }) {
  const hook = useGitHook(projectId, appId);
  const [form, setForm] = useState({ repo: "", branch: "main", dockerfile: "", watchPaths: "" });
  const [editing, setEditing] = useState(false);
  const [revealedSecret, setRevealedSecret] = useState<string | null>(null);
  // 编辑入口（SetGitHook 是全量 upsert：改配置不换 Token）——表单按现值
  // 预填，保存即回展示态；创建入口（未配置空态）回空白初值。
  const startEdit = () => {
    setForm({
      repo: hook.data?.repo ?? "",
      branch: hook.data?.branch ?? "main",
      dockerfile: hook.data?.dockerfile ?? "",
      watchPaths: (hook.data?.watch_paths ?? []).join(", "),
    });
    setEditing(true);
  };
  const startCreate = () => {
    setForm({ repo: "", branch: "main", dockerfile: "", watchPaths: "" });
    setEditing(true);
  };

  const commands = appTabCommands(projectId, appId);
  // scope 显式传（{projectId, appId}）：hook 读面已迁 kit record 键——命令
  // 失效 selector 与键 scope 必须相等（appTabCommands 的 appId 在闭包不进
  // scope，缺省 {} 与键不相交——收尾批测试实咬）。
  const commandScope = { projectId, appId };
  const save = useCommand(commands.setGitHook, {
    scope: commandScope,
    onSettled: toastOnSettled((payload) => {
      setEditing(false);
      toast("Git deploy hook configured");
      if (payload.secret) setRevealedSecret(payload.secret);
    }),
  });
  const rotate = useCommand(commands.rotateGitHook, {
    scope: commandScope,
    onSettled: toastOnSettled((payload) => {
      toast("Hook token rotated — the previous token is revoked");
      if (payload.secret) setRevealedSecret(payload.secret);
    }),
  });
  // Deploy now：拉取通道（RefreshGitHook）——不依赖 GitHub 侧 webhook 配置
  // 即可立即部署（首配验证 / App Templates 第三方模板仓的前提原语）。
  const refresh = useCommand(commands.refreshGitHook, {
    scope: commandScope,
    onSettled: toastOnSettled((payload) => {
      const suffix = payload.admission?.outcome === "deduplicated" ? " (deduplicated — same commit already in the pipeline)" : "";
      toast(`Deployment ${payload.deployment_id ?? "?"} queued @ ${payload.commit?.slice(0, 7) ?? "?"}${suffix}`);
    }),
  });
  // Remove：解除通道（DeleteGitHook，W-8）——token 与 webhook secret 即刻
  // 失效，材料所有权归还 deploy 面。非幂等（无 hook 即 404）。
  const remove = useCommand(commands.deleteGitHook, {
    scope: commandScope,
    onSettled: toastOnSettled(() => toast("Git deploy hook removed — material ownership returned to deploys")),
  });

  const configured = hook.data != null;
  return (
    <section className="rounded-xl border bg-panel p-4">
      <h3 className="mb-3 text-sm font-semibold">Git deploy hook</h3>
      <p className="mb-3 rounded-lg border bg-panel-2/40 px-3 py-2 text-2xs text-muted">
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
        <p className="text-xs text-muted">Loading hook…</p>
      ) : configured && !editing ? (
        <>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
            <dt className="text-muted">repo</dt>
            <dd className="font-mono">{hook.data!.repo ?? "—"}</dd>
            <dt className="text-muted">branch</dt>
            <dd className="font-mono">{hook.data!.branch ?? "—"}</dd>
            <dt className="text-muted">dockerfile</dt>
            <dd className="font-mono">{hook.data!.dockerfile || "—"}</dd>
            <dt className="text-muted">token</dt>
            <dd className="font-mono">{hook.data!.token_prefix ?? "—"}…</dd>
            <dt className="text-muted">updated</dt>
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
            <Button size="sm" disabled={refresh.pending} onClick={() => refresh.mutate()}>
              {refresh.pending ? "Pulling…" : "Deploy now"}
            </Button>
            <Button size="sm" variant="outline" onClick={startEdit}>
              Edit
            </Button>
            <Button size="sm" variant="outline" disabled={rotate.pending} onClick={() => rotate.mutate()}>
              Rotate token…
            </Button>
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button size="sm" variant="outline" className="text-err" disabled={remove.pending}>
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
                    disabled={remove.pending}
                    onClick={(event) => {
                      event.preventDefault();
                      remove.mutate();
                    }}
                  >
                    {remove.pending ? "Removing…" : "Remove"}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </div>
          <p className="mt-2 text-2xs text-muted">
            <span className="font-semibold text-fg">Deploy now</span> pulls the branch HEAD and deploys immediately — no
            webhook required (template repos, first config). A <span className="font-mono">.yml/.yaml</span> path is taken as a
            compose file (its services become processes; images deploy directly); anything else builds as a Dockerfile. For push
            triggers, paste the webhook URL (revealed at configure/rotate time) into GitHub → Webhooks; the secret doubles as the
            signing key.
          </p>
        </>
      ) : editing ? (
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            save.mutate({ repo: form.repo, branch: form.branch, dockerfile: form.dockerfile, watchPaths: form.watchPaths });
          }}
        >
          {configured ? (
            <p className="rounded-lg border bg-panel-2/40 px-3 py-2 text-2xs text-muted">
              Editing the existing hook — SetGitHook is a full upsert: fields become this save&apos;s values, the token stays
              unchanged. Repo URL must keep its scheme (https).
            </p>
          ) : (
            <p className="rounded-lg border bg-panel-2/40 px-3 py-2 text-2xs text-muted">
              Configuring a hook hands the material half of the spec (source, build) to git — repo URL keeps its scheme
              (https).
            </p>
          )}
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
            <Button type="submit" size="sm" disabled={save.pending || form.repo === ""}>
              {save.pending ? "Saving…" : configured ? "Save changes" : "Configure hook"}
            </Button>
            <Button type="button" size="sm" variant="outline" onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </div>
        </form>
      ) : (
        <div className="flex flex-col gap-3">
          <p className="text-xs leading-relaxed text-muted">
            Not configured — deploys own the material half of this app&apos;s spec (source, build). Configuring a hook hands
            that half to git: pushes redeploy automatically while your runtime settings stay put.
          </p>
          <div>
            <Button size="sm" variant="outline" onClick={startCreate}>
              Configure hook…
            </Button>
          </div>
        </div>
      )}
      {revealedSecret != null ? (
        <div className="mt-3 rounded-md border border-warning/40 bg-warning/10 p-2.5 text-xs">
          <p className="font-semibold">Webhook URL — shown once, paste it into GitHub (repo or GitHub App → Webhooks)</p>
          <p className="mt-1 flex items-center gap-1 font-mono text-2xs">
            {`${window.location.origin}/v1/hooks/${revealedSecret}`}
            <CopyButton value={`${window.location.origin}/v1/hooks/${revealedSecret}`} />
          </p>
          <p className="mt-1 text-2xs text-muted">
            The token doubles as the webhook signing key (secret field). Rotating revokes the old URL immediately. Or skip
            webhooks entirely — Deploy now above pulls and deploys without GitHub-side configuration.
          </p>
        </div>
      ) : null}
      {hook.isError ? <p className="mt-3 text-xs text-err">{fieldError(hook.error)}</p> : null}
    </section>
  );
}

function DangerZone({ appId, appName, projectId, onDeleted }: { appId: string; appName: string; projectId: string; onDeleted: () => void }) {
  const remove = useCommand(appTabCommands(projectId, appId).deleteApp, {
    scope: { projectId },
    onSettled: toastOnSettled(() => {
      toast(`App ${appName} deleted`);
      onDeleted();
    }),
  });
  return (
    <section className="rounded-xl border border-err/30 bg-panel p-5">
      <h3 className="mb-2 text-sm font-semibold text-err">Danger zone</h3>
      <p className="mb-4 text-xs text-muted">
        Deleting <span className="font-semibold text-fg">{appName}</span> removes its deployments history, routes and exec
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
              disabled={remove.pending}
              onClick={(event) => {
                event.preventDefault();
                remove.mutate();
              }}
            >
              {remove.pending ? "Deleting…" : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}
