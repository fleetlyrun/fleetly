import { FingerprintIcon, InfoIcon, PlusIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { useCollection, useCollectionData, useCommand } from "@lynx-go/uikit-core";

import { describeApiError } from "@/kit/errors";
import { githubAppReposResource, githubAppsResource, githubAppsSearch, type GitHubAppRow } from "@/kit/githubapps";
import { toastOnSettled } from "@/kit/toast";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { CopyButton } from "@/components/domain/copy-button";
import { RelativeTime } from "@/components/domain/relative-time";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
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
import { describeError } from "@/lib/api-errors";


// GitHubAppsView 是 GitHub App 接入材料的管理卡（ADR-0073 阶段 B，视觉
// 真源 docs/design/2026-10-13-github-app-material-prototype.html——列表行
// /Repos 验证面板/添加对话框三段逐屏回照）。projectId 在场 = Project 级
// 行（Registry 第三 tab）；缺席 = 平台级 Global 行（Fleet 段管理页）。
// 私钥只写不读：任何界面只出现指纹。
// 2026-10-26 批1 Wave2-A useCollection 化 + 批2 写面 useCommand 化：列表读
// 面走 kit 资源（快筛/分页进 URL；无轮询——与旧 inline 查询同现状）；
// Repos 展开子行走 DataTable 本地扩展槽 renderExtraRow（展开态归页面）；
// 写面三动词（put/remove/manifestStart）走 kit/githubapps.ts 的 resource
// commands——批1 的写后双失效特判（kit 集合前缀 + ["github-apps"] 前缀）
// 退役，统一由 commands 的 invalidates（allScopes 双语境集合）+ legacyKeys
//（repos 子查询键）声明承载；manifest 整页 POST 流（浏览器表单导航）非
// mutation 不动；toast 单源（ok 原句 / rejected·conflict toast.error
// (message) / failure 走 describeApiError——各命令内联，payload 相关文案
// 不合模板）。

export function GitHubAppsView({ projectId, from }: { projectId?: string; from: string }) {
  const list = useCollection(githubAppsResource, {
    from,
    search: githubAppsSearch,
    // scope 可选轴：project 语境传 projectId 分面；global 面不传（资源未
    // 声明 scope，context 采纳不生效）
    scope: projectId != null ? { projectId } : undefined,
  });
  const [addOpen, setAddOpen] = useState(false);
  const [deleting, setDeleting] = useState<GitHubAppRow | null>(null);
  // Repos 展开态归页面（单开；id 空兜底不展开）
  const [openRepos, setOpenRepos] = useState<string | null>(null);
  // 失效（双语境集合 + repos 子查询键）由 remove 命令声明承载——确认壳
  // 两分支都要收窗（旧 onError 也收）。
  const del = useCommand(githubAppsResource.commands.remove, {
    onSettled: (result, failure) => {
      if (result != null || failure != null) setDeleting(null);
      if (failure != null) {
        toast.error(describeApiError(failure));
        return;
      }
      if (result?.status === "ok") toast.success("GitHub App removed — clone resolution loses this row immediately");
      else if (result != null) toast.error(result.message);
    },
  });

  const columns: LegacyColumnDef<GitHubAppRow, any>[] = [
    {
      accessorKey: "account_login",
      header: "Owner",
      meta: { sortField: "account_login" },
      cell: ({ row }) => (
        <div className="flex items-center gap-2.5">
          <div className="flex size-7 flex-none items-center justify-center rounded-lg bg-panel-2 font-semibold">
            {(row.original.account_login ?? "?").slice(0, 1).toUpperCase()}
          </div>
          <div>
            <div className="flex items-center gap-1.5 text-sm font-semibold">
              {row.original.account_login}
              <span className="rounded border px-1 py-px font-mono text-2xs text-muted">
                {row.original.project_id ? "PROJECT" : "GLOBAL"}
              </span>
            </div>
            <div className="font-mono text-2xs text-muted">{row.original.api_base || "api.github.com"}</div>
          </div>
        </div>
      ),
    },
    { accessorKey: "app_id", header: "App ID", cell: ({ row }) => <span className="font-mono text-xs">{row.original.app_id}</span> },
    {
      accessorKey: "installation_id",
      header: "Installation ID",
      cell: ({ row }) => <span className="font-mono text-xs">{row.original.installation_id}</span>,
    },
    {
      id: "fingerprint",
      header: "Private key",
      cell: ({ row }) => (
        <span className="flex items-center gap-1 font-mono text-xs text-muted" title="key never leaves the store — fingerprint only">
          <CopyButton value={row.original.private_key_fingerprint ?? ""} />
          {row.original.private_key_fingerprint}
        </span>
      ),
    },
    {
      accessorKey: "created_at",
      header: "Added",
      cell: ({ row }) => (
        <span className="text-xs text-muted">
          <RelativeTime value={row.original.created_at} />
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => (
        <div className="flex items-center justify-end gap-1.5">
          <Button
            variant="outline"
            size="sm"
            onClick={() => setOpenRepos(openRepos === row.original.id ? null : (row.original.id ?? null))}
          >
            {openRepos === row.original.id ? "Repos ▴" : "Repos ▾"}
          </Button>
          <Button variant="outline" size="sm" className="text-[var(--status-danger)]" onClick={() => setDeleting(row.original)}>
            Delete
          </Button>
        </div>
      ),
    },
  ];

  const filtered = list.query.predicate != null;
  return (
    <>
      <ListPage
        list={list}
        toolbar={
          <div className="flex flex-wrap items-center justify-between gap-2">
            <FilterBar list={list} schema={githubAppsSearch} textPlaceholder="Filter by owner..." />
            <div className="py-2 pr-3">
              <Button size="sm" disabled={addOpen} onClick={() => setAddOpen(true)}>
                <PlusIcon data-icon-start-inline />
                Add GitHub App
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No GitHub Apps match the filter" : projectId != null ? "No GitHub Apps in this project" : "No global GitHub Apps"}
        emptyDescription={
          filtered
            ? "Adjust or clear the filter to see rows again."
            : projectId != null
              ? "Private repositories resolve anonymous clones today — builds fail with an auth error and a pointer here. Add a project-scoped app, or rely on a global one if your platform provides it."
              : "Global installations serve every project at clone time. Add one with a platform-admin token."
        }
      >
        <DataTable
          list={list}
          columns={columns}
          getRowId={(row) => row.id ?? ""}
          renderExtraRow={(app) => (openRepos != null && app.id === openRepos ? <ReposPanel appId={app.id ?? ""} /> : null)}
        />
      </ListPage>
      {projectId != null ? (
        <p className="mt-3 text-2xs text-muted">
          Build-time resolution order: <span className="font-medium">project GitHub App</span> →{" "}
          <span className="font-medium">global GitHub App</span> → anonymous clone. A private repo without material fails with
          an auth error and a pointer here; rotating a key is a re-put away (no redeploy).
        </p>
      ) : null}

      {addOpen ? (
        <AddGitHubAppDialog
          projectId={projectId}
          onClose={() => setAddOpen(false)}
          // 批2：添加成功后的失效由 put 命令声明承载，这里只收弹窗
          onAdded={() => setAddOpen(false)}
        />
      ) : null}
      <ConfirmDialog
        open={deleting != null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`Remove ${deleting?.account_login ?? ""}?`}
        description="Clones stop using this row immediately; the installation on GitHub is untouched. Private repos it covered will fail with an auth error until a row is added again."
        confirmLabel="Remove app"
        pendingLabel="Removing…"
        risk={{ tier: "standard" }}
        onConfirm={() => (deleting != null ? del.mutateAsync({ id: deleting.id ?? "" }) : Promise.resolve())}
      />
    </>
  );
}

// ReposPanel 是 installation 可达仓库的展开面板（现铸 token 的健康验证面
// ——挂起/卸载的安装在这里以 E_GITHUB_UPSTREAM 诚实呈现）。renderExtraRow
// 槽内自持取数（展开挂载即取、收起即停）。
function ReposPanel({ appId }: { appId: string }) {
  // repos 展开伴读（M1 useCollectionData）：retry:false 同旧，展开生命周期归页面。
  const repos = useCollectionData(githubAppReposResource, {
    scope: { appId },
    window: { size: 200 },
    enabled: appId !== "",
    retry: false,
  });
  return (
    <div className="bg-panel-2/30 px-4 pb-4 pt-3 sm:pl-14">
      <div className="mb-2 flex items-center gap-2">
        <span className="text-2xs font-semibold uppercase tracking-wide text-muted">
          Repositories reachable via this installation
        </span>
        <span className="font-mono text-2xs text-muted">resolved live · not stored</span>
      </div>
      {repos.status === "loading" ? (
        <p className="text-xs text-muted">Minting a short-lived token and listing…</p>
      ) : repos.status === "error" ? (
        <p className="rounded-lg border bg-panel px-3 py-2 text-xs text-[var(--status-danger)]">
          {describeError(repos.error).title} — {describeError(repos.error).detail}
        </p>
      ) : repos.rows.length === 0 ? (
        <p className="text-xs text-muted">No repositories reachable via this installation.</p>
      ) : (
        <div className="overflow-hidden rounded-lg border bg-panel">
          {repos.rows.map((repo) => (
            <div key={repo.full_name} className="flex items-center gap-2 border-b px-3 py-1.5 text-xs last:border-b-0">
              <span className="font-mono">{repo.full_name}</span>
              <span
                className={`rounded border px-1 py-px font-mono text-2xs ${
                  repo.private ? "border-[color-mix(in_oklch,var(--status-warn)_35%,transparent)] text-[var(--status-warn)]" : "text-muted"
                }`}
              >
                {repo.private ? "private" : "public"}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// AddGitHubAppDialog 是添加表单（原型回照：App ID / Installation ID /
// 私钥三字段 + Advanced 折叠 api_base；Owner 受理即验证自动解析——保存
// 时平台实调 GitHub，错值当场报）。批2：put/manifestStart 走 kit commands，
// 表单壳（useState + textarea 折叠）保留；错误反馈 toast 单源（旧内联
// error <p> 退役）。
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
  const startFlow = useCommand(githubAppsResource.commands.manifestStart, {
    onSettled: (result, failure) => {
      if (failure != null) {
        toast.error(describeApiError(failure));
        return;
      }
      if (result == null) return;
      if (result.status === "ok") {
        const { action_url: actionUrl, manifest } = result.payload;
        if (actionUrl != null && actionUrl !== "" && manifest != null && manifest !== "") {
          setGhFlow({ actionUrl, manifest });
        }
      } else {
        toast.error(result.message);
      }
    },
  });

  // put 的 ok 文案带受理解析出的 owner（载荷相关）——toastOnSettled 的
  // onOk(payload) 支路；del/startFlow 各有壳编排（收窗/流材料条件）保持
  // 内联同口径。
  const put = useCommand(githubAppsResource.commands.put, {
    onSettled: toastOnSettled((payload) => {
      toast.success(`GitHub App added — owner ${payload.app?.account_login ?? "?"} resolved from the installation`);
      onAdded();
    }),
  });

  const valid = appId.trim() !== "" && installationId.trim() !== "" && privateKey.trim() !== "";

  // 表单材料就绪 = 渲染自动提交页（整页 POST 导航到 GitHub 创建表单，
  // 预填 name/权限/回跳；用户确认后 GitHub 302 回 /github-apps/connect）。
  if (ghFlow != null) {
    return (
      <div className="flex min-h-svh flex-col items-center justify-center gap-3 text-sm text-muted">
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
        <div className="flex flex-col gap-2 rounded-lg border bg-panel-2/30 p-3">
          <div className="text-xs font-semibold">Recommended — create it on GitHub automatically</div>
          <p className="text-2xs text-muted">
            GitHub generates the key and delivers it to fleetly (sealed, never shown) — nothing to copy by hand.
          </p>
          <Button
            size="sm"
            className="self-start"
            disabled={startFlow.pending}
            onClick={() => startFlow.mutate({ projectId })}
          >
            <FingerprintIcon data-icon-start-inline />
            {startFlow.pending ? "Preparing…" : "Continue to GitHub →"}
          </Button>
          <div className="pt-1 text-2xs text-muted">
            or configure manually below (GitHub Enterprise Server / restricted environments)
          </div>
        </div>
        <div className="flex flex-col gap-3">
          <label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">App ID</span>
            <Input value={appId} onChange={(e) => setAppId(e.target.value)} placeholder="182345" inputMode="numeric" />
            <span className="text-2xs text-muted">GitHub App → General → About → App ID (numeric).</span>
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">Installation ID</span>
            <Input
              value={installationId}
              onChange={(e) => setInstallationId(e.target.value)}
              placeholder="51790921"
              inputMode="numeric"
            />
            <span className="text-2xs text-muted">Install the app on your account, then take the ID from the installation URL.</span>
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">
              Private key <span className="font-normal text-muted">(PEM — write-only)</span>
            </span>
            <textarea
              className="min-h-24 rounded-lg border bg-transparent px-3 py-2 font-mono text-xs"
              placeholder={"-----BEGIN RSA PRIVATE KEY-----\n…\n-----END RSA PRIVATE KEY-----"}
              value={privateKey}
              onChange={(e) => setPrivateKey(e.target.value)}
            />
            <span className="text-2xs text-muted">
              Sealed with the platform master key at rest. Never returned by the API — only a fingerprint stays visible.
            </span>
          </label>
          <div>
            <button
              type="button"
              className="text-xs font-semibold text-muted hover:text-fg"
              onClick={() => setAdvanced(!advanced)}
            >
              {advanced ? "▾" : "▸"} Advanced · API base for GitHub Enterprise Server
            </button>
            {advanced ? (
              <label className="mt-2 flex flex-col gap-1.5">
                <Input value={apiBase} onChange={(e) => setApiBase(e.target.value)} placeholder="https://api.github.com" />
                <span className="text-2xs text-muted">
                  Leave empty for github.com. GHES example: <span className="font-mono">https://ghe.example.com/api/v3</span>.
                </span>
              </label>
            ) : null}
          </div>
          <p className="flex items-start gap-1.5 rounded-lg border bg-panel-2/40 px-3 py-2 text-2xs text-muted">
            <InfoIcon className="mt-px size-3.5 flex-none" />
            On save, fleetly verifies the app + installation + key against GitHub and fills in the owner automatically — repos
            under that owner resolve to this app{projectId != null ? " (project rows shadow global rows)" : ""}.
          </p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={!valid || put.pending}
            onClick={() =>
              put.mutate({
                appId,
                installationId,
                privateKeyPem: privateKey,
                apiBase,
                projectId,
              })
            }
          >
            {put.pending ? "Verifying…" : "Add app"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
