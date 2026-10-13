import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FingerprintIcon, InfoIcon, PlusIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import type { components as ghSchemas } from "@/api/github_apps";
import { apiFetch, apiSend } from "@/api/client";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { ErrorState } from "@/components/domain/error-state";
import { ListCard } from "@/components/domain/list-page";
import { RelativeTime } from "@/components/domain/relative-time";
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
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { describeError } from "@/lib/api-errors";

type GitHubApp = ghSchemas["schemas"]["v1GitHubApp"];
type GitHubAppRepo = ghSchemas["schemas"]["v1GitHubAppRepo"];

// GitHubAppsView 是 GitHub App 接入材料的管理卡（ADR-0073 阶段 B，视觉
// 真源 docs/design/2026-10-13-github-app-material-prototype.html——列表行
// /Repos 验证面板/添加对话框三段逐屏回照）。projectId 在场 = Project 级
// 行（Registry 第三 tab）；缺席 = 平台级 Global 行（Fleet 段管理页）。
// 私钥只写不读：任何界面只出现指纹。
export function GitHubAppsView({ projectId }: { projectId?: string }) {
  const queryClient = useQueryClient();
  const listKey = ["github-apps", projectId ?? "global"];
  const list = useQuery({
    queryKey: listKey,
    queryFn: async (): Promise<GitHubApp[]> => {
      const qs = projectId != null ? `project_id=${encodeURIComponent(projectId)}` : "global=true";
      const res = await apiFetch<{ apps?: Array<GitHubApp | undefined> }>(`/v1/github-apps?${qs}`);
      return (res.apps ?? []).flatMap((entry) => (entry != null ? [entry] : []));
    },
  });
  const [addOpen, setAddOpen] = useState(false);
  const [deleting, setDeleting] = useState<GitHubApp | null>(null);
  const [openRepos, setOpenRepos] = useState<string | null>(null);
  const del = useMutation({
    mutationFn: (row: GitHubApp) => apiSend(`/v1/github-apps/${encodeURIComponent(row.id ?? "")}`, "DELETE", {}),
    onSuccess: () => {
      toast.success("GitHub App removed — clone resolution loses this row immediately");
      void queryClient.invalidateQueries({ queryKey: ["github-apps"] });
      setDeleting(null);
    },
    onError: (cause) => {
      toast.error(`${describeError(cause).title} — ${describeError(cause).detail}`);
      setDeleting(null);
    },
  });

  return (
    <>
      <ListCard
        label="github apps"
        placeholder="Filter by owner..."
        rows={list.data ?? []}
        fieldsOf={(app) => [app.account_login ?? "", app.id ?? ""]}
        actions={
          <Button size="sm" disabled={addOpen} onClick={() => setAddOpen(true)}>
            <PlusIcon data-icon-start-inline />
            Add GitHub App
          </Button>
        }
        empty={
          <EmptyState
            icon={FingerprintIcon}
            title={projectId != null ? "No GitHub Apps in this project" : "No global GitHub Apps"}
            description={
              projectId != null
                ? "Private repositories resolve anonymous clones today — builds fail with an auth error and a pointer here. Add a project-scoped app, or rely on a global one if your platform provides it."
                : "Global installations serve every project at clone time. Add one with a platform-admin token."
            }
          />
        }
      >
        {({ pageRows }) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Owner</TableHead>
                <TableHead>App ID</TableHead>
                <TableHead>Installation</TableHead>
                <TableHead>Private key</TableHead>
                <TableHead>Added</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.isPending ? (
                Array.from({ length: 1 }).map((_, index) => (
                  <TableRow key={index}>
                    {Array.from({ length: 6 }).map((_, cell) => (
                      <TableCell key={cell}>
                        <Skeleton className="h-5 w-24" />
                      </TableCell>
                    ))}
                  </TableRow>
                ))
              ) : list.isError ? (
                <TableRow>
                  <TableCell colSpan={6}>
                    <ErrorState error={list.error} onRetry={() => void list.refetch()} />
                  </TableCell>
                </TableRow>
              ) : pageRows.length === 0 ? null : (
                pageRows.map((app) => (
                  <GitHubAppRow
                    key={app.id}
                    app={app}
                    reposOpen={openRepos === app.id}
                    onToggleRepos={() => setOpenRepos(openRepos === app.id ? null : (app.id ?? null))}
                    onDelete={() => setDeleting(app)}
                  />
                ))
              )}
            </TableBody>
          </Table>
        )}
      </ListCard>
      {projectId != null ? (
        <p className="mt-3 text-2xs text-muted-foreground">
          Build-time resolution order: <span className="font-medium">project GitHub App</span> →{" "}
          <span className="font-medium">global GitHub App</span> → anonymous clone. A private repo without material fails with
          an auth error and a pointer here; rotating a key is a re-put away (no redeploy).
        </p>
      ) : null}

      {addOpen ? (
        <AddGitHubAppDialog
          projectId={projectId}
          onClose={() => setAddOpen(false)}
          onAdded={() => {
            setAddOpen(false);
            void queryClient.invalidateQueries({ queryKey: ["github-apps"] });
          }}
        />
      ) : null}
      <ConfirmDialog
        open={deleting != null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`Remove ${deleting?.account_login ?? ""}?`}
        description="Clones stop using this row immediately; the installation on GitHub is untouched. Private repos it covered will fail with an auth error until a row is added again."
        confirmLabel="Remove app"
        pendingLabel="Removing…"
        onConfirm={() => (deleting != null ? del.mutateAsync(deleting) : Promise.resolve())}
      />
    </>
  );
}

function GitHubAppRow({
  app,
  reposOpen,
  onToggleRepos,
  onDelete,
}: {
  app: GitHubApp;
  reposOpen: boolean;
  onToggleRepos: () => void;
  onDelete: () => void;
}) {
  return (
    <>
      <TableRow>
        <TableCell>
          <div className="flex items-center gap-2.5">
            <div className="flex size-7 flex-none items-center justify-center rounded-lg bg-muted font-semibold">
              {(app.account_login ?? "?").slice(0, 1).toUpperCase()}
            </div>
            <div>
              <div className="flex items-center gap-1.5 text-sm font-semibold">
                {app.account_login}
                <span className="rounded border px-1 py-px font-mono text-2xs text-muted-foreground">
                  {app.project_id ? "PROJECT" : "GLOBAL"}
                </span>
              </div>
              <div className="font-mono text-2xs text-muted-foreground">{app.api_base || "api.github.com"}</div>
            </div>
          </div>
        </TableCell>
        <TableCell className="font-mono text-xs">{app.app_id}</TableCell>
        <TableCell className="font-mono text-xs">{app.installation_id}</TableCell>
        <TableCell>
          <span className="flex items-center gap-1 font-mono text-xs text-muted-foreground" title="key never leaves the store — fingerprint only">
            <CopyButton value={app.private_key_fingerprint ?? ""} />
            {app.private_key_fingerprint}
          </span>
        </TableCell>
        <TableCell className="text-xs text-muted-foreground">
          <RelativeTime value={app.created_at} />
        </TableCell>
        <TableCell className="text-right">
          <div className="flex items-center justify-end gap-1.5">
            <Button variant="outline" size="sm" onClick={onToggleRepos}>
              {reposOpen ? "Repos ▴" : "Repos ▾"}
            </Button>
            <Button variant="outline" size="sm" className="text-[var(--status-danger)]" onClick={onDelete}>
              Delete
            </Button>
          </div>
        </TableCell>
      </TableRow>
      {reposOpen ? <ReposRow appId={app.id ?? ""} /> : null}
    </>
  );
}

// ReposRow 是 installation 可达仓库的展开面板（现铸 token 的健康验证面—
// 挂起/卸载的安装在这里以 E_GITHUB_UPSTREAM 诚实呈现）。
function ReposRow({ appId }: { appId: string }) {
  const repos = useQuery({
    queryKey: ["github-apps", appId, "repos"],
    queryFn: async (): Promise<GitHubAppRepo[]> => {
      const res = await apiFetch<{ repos?: Array<GitHubAppRepo | undefined> }>(
        `/v1/github-apps/${encodeURIComponent(appId)}/repos`,
      );
      return (res.repos ?? []).flatMap((entry) => (entry != null ? [entry] : []));
    },
    retry: false,
  });
  return (
    <TableRow className="hover:bg-transparent">
      <TableCell colSpan={6} className="bg-muted/30 px-4 pb-4 pt-3 sm:pl-14">
        <div className="mb-2 flex items-center gap-2">
          <span className="text-2xs font-semibold uppercase tracking-wide text-muted-foreground">
            Repositories reachable via this installation
          </span>
          <span className="font-mono text-2xs text-muted-foreground">resolved live · not stored</span>
        </div>
        {repos.isPending ? (
          <p className="text-xs text-muted-foreground">Minting a short-lived token and listing…</p>
        ) : repos.isError ? (
          <p className="rounded-lg border bg-card px-3 py-2 text-xs text-[var(--status-danger)]">
            {describeError(repos.error).title} — {describeError(repos.error).detail}
          </p>
        ) : (repos.data ?? []).length === 0 ? (
          <p className="text-xs text-muted-foreground">No repositories reachable via this installation.</p>
        ) : (
          <div className="overflow-hidden rounded-lg border bg-card">
            {(repos.data ?? []).map((repo) => (
              <div key={repo.full_name} className="flex items-center gap-2 border-b px-3 py-1.5 text-xs last:border-b-0">
                <span className="font-mono">{repo.full_name}</span>
                <span
                  className={`rounded border px-1 py-px font-mono text-2xs ${
                    repo.private ? "border-[color-mix(in_oklch,var(--status-warn)_35%,transparent)] text-[var(--status-warn)]" : "text-muted-foreground"
                  }`}
                >
                  {repo.private ? "private" : "public"}
                </span>
              </div>
            ))}
          </div>
        )}
      </TableCell>
    </TableRow>
  );
}

// AddGitHubAppDialog 是添加表单（原型回照：App ID / Installation ID /
// 私钥三字段 + Advanced 折叠 api_base；Owner 受理即验证自动解析——保存
// 时平台实调 GitHub，错值当场报）。
function AddGitHubAppDialog({
  projectId,
  onClose,
  onAdded,
}: {
  projectId?: string;
  onClose: () => void;
  onAdded: () => void;
}) {
  const [appId, setAppId] = useState("");
  const [installationId, setInstallationId] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [apiBase, setApiBase] = useState("");
  const [advanced, setAdvanced] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // startFlow 发起 manifest 自动建流（ADR-0073 决策 8）。提交形态（GitHub
  // 官方文档口径）：浏览器以 HTML form **POST** 到 apps/new（字段名
  // manifest、值为 JSON 字符串）——GET 跳转带 query 会被 GitHub 静默忽略、
  // 表单全空（真机两连咬的根因）。创建/安装完成后经 /github-apps/connect
  // 回跳落行。公网 console 基址未配置时服务端精确报错——手动表单即兜底。
  const [ghFlow, setGhFlow] = useState<{ actionUrl: string; manifest: string } | null>(null);
  const flowFormRef = useRef<HTMLFormElement>(null);
  useEffect(() => {
    if (ghFlow != null) flowFormRef.current?.submit();
  }, [ghFlow]);
  const startFlow = useMutation({
    mutationFn: () => {
      const body = projectId != null ? { project_id: projectId } : { global: true };
      return apiSend<{ action_url?: string; manifest?: string }>("/v1/github-apps/manifest/start", "POST", body);
    },
    onSuccess: (res) => {
      if (res.action_url != null && res.action_url !== "" && res.manifest != null && res.manifest !== "") {
        setGhFlow({ actionUrl: res.action_url, manifest: res.manifest });
      }
    },
  });

  const put = useMutation({
    mutationFn: async () => {
      const body: Record<string, unknown> = {
        app_id: Number(appId),
        installation_id: Number(installationId),
        private_key_pem: privateKey,
      };
      if (projectId != null) {
        body.project_id = projectId;
      } else {
        body.global = true;
      }
      if (apiBase.trim() !== "") {
        body.api_base = apiBase.trim();
      }
      return apiSend<{ app?: GitHubApp }>("/v1/github-apps", "POST", body);
    },
    onSuccess: (res) => {
      toast.success(`GitHub App added — owner ${res.app?.account_login ?? "?"} resolved from the installation`);
      onAdded();
    },
    onError: (cause) => setError(`${describeError(cause).title} — ${describeError(cause).detail}`),
  });

  const valid = appId.trim() !== "" && installationId.trim() !== "" && privateKey.trim() !== "";

  // 表单材料就绪 = 渲染自动提交页（整页 POST 导航到 GitHub 创建表单，
  // 预填 name/权限/回跳；用户确认后 GitHub 302 回 /github-apps/connect）。
  if (ghFlow != null) {
    return (
      <div className="flex min-h-svh flex-col items-center justify-center gap-3 text-sm text-muted-foreground">
        <p>Redirecting to GitHub…</p>
        <form ref={flowFormRef} method="POST" action={ghFlow.actionUrl}>
          <input type="hidden" name="manifest" value={ghFlow.manifest} />
        </form>
      </div>
    );
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Add a GitHub App</DialogTitle>
          <DialogDescription>
            Private-repo access material for builds — a short-lived installation token is minted per clone and never stored in
            revisions.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-2 rounded-lg border bg-muted/30 p-3">
          <div className="text-xs font-semibold">Recommended — create it on GitHub automatically</div>
          <p className="text-2xs text-muted-foreground">
            GitHub generates the key and delivers it to fleetly (sealed, never shown) — nothing to copy by hand.
          </p>
          <Button
            size="sm"
            className="self-start"
            disabled={startFlow.isPending}
            onClick={() =>
              startFlow.mutate(undefined, {
                onError: (cause) => toast.error(`${describeError(cause).title} — ${describeError(cause).detail}`),
              })
            }
          >
            <FingerprintIcon data-icon-start-inline />
            {startFlow.isPending ? "Preparing…" : "Continue to GitHub →"}
          </Button>
          <div className="pt-1 text-2xs text-muted-foreground">
            or configure manually below (GitHub Enterprise Server / restricted environments)
          </div>
        </div>
        <div className="flex flex-col gap-3">
          <label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">App ID</span>
            <Input value={appId} onChange={(e) => setAppId(e.target.value)} placeholder="182345" inputMode="numeric" />
            <span className="text-2xs text-muted-foreground">GitHub App → General → About → App ID (numeric).</span>
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">Installation ID</span>
            <Input
              value={installationId}
              onChange={(e) => setInstallationId(e.target.value)}
              placeholder="51790921"
              inputMode="numeric"
            />
            <span className="text-2xs text-muted-foreground">Install the app on your account, then take the ID from the installation URL.</span>
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">
              Private key <span className="font-normal text-muted-foreground">(PEM — write-only)</span>
            </span>
            <textarea
              className="min-h-24 rounded-lg border bg-transparent px-3 py-2 font-mono text-xs"
              placeholder={"-----BEGIN RSA PRIVATE KEY-----\n…\n-----END RSA PRIVATE KEY-----"}
              value={privateKey}
              onChange={(e) => setPrivateKey(e.target.value)}
            />
            <span className="text-2xs text-muted-foreground">
              Sealed with the platform master key at rest. Never returned by the API — only a fingerprint stays visible.
            </span>
          </label>
          <div>
            <button
              type="button"
              className="text-xs font-semibold text-muted-foreground hover:text-foreground"
              onClick={() => setAdvanced(!advanced)}
            >
              {advanced ? "▾" : "▸"} Advanced · API base for GitHub Enterprise Server
            </button>
            {advanced ? (
              <label className="mt-2 flex flex-col gap-1.5">
                <Input value={apiBase} onChange={(e) => setApiBase(e.target.value)} placeholder="https://api.github.com" />
                <span className="text-2xs text-muted-foreground">
                  Leave empty for github.com. GHES example: <span className="font-mono">https://ghe.example.com/api/v3</span>.
                </span>
              </label>
            ) : null}
          </div>
          <p className="flex items-start gap-1.5 rounded-lg border bg-muted/40 px-3 py-2 text-2xs text-muted-foreground">
            <InfoIcon className="mt-px size-3.5 flex-none" />
            On save, fleetly verifies the app + installation + key against GitHub and fills in the owner automatically — repos
            under that owner resolve to this app{projectId != null ? " (project rows shadow global rows)" : ""}.
          </p>
          {error != null ? (
            <p className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 text-xs text-[var(--status-danger)]">
              {error}
            </p>
          ) : null}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={!valid || put.isPending} onClick={() => put.mutate()}>
            {put.isPending ? "Verifying…" : "Add app"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
