import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { DatabaseBackupIcon, KeyRound, PlusIcon, ShieldOffIcon } from "lucide-react";
import { toast } from "sonner";
import { collectionQueryOptions, useCommand, useRecord } from "@lynx-go/uikit-core";
import type { CollectionQuery } from "@lynx-go/uikit-core/kernel";
import { rolesResource } from "@/kit/identity";
import {
  freezesResource,
  platformBackupsResource,
  sessionResource,
  tokensResource,
} from "@/kit/settings";
import { toastOnSettled } from "@/kit/toast";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { ErrorState } from "@/components/domain/error-state";
import { PageFrame } from "@/components/domain/page-frame";
import { SectionCard } from "@/components/domain/section-card";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge } from "@/components/domain/status-badge";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
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
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

// 设置页（F3.1 → UI v2 批 5 reskin；模板化收编 PageFrame(narrow) +
// SectionCard）：身份（whoami）/ Token 铸造与吊销（Console 自身凭证生命
// 周期；secret 一次性揭示）/ 平台治理（变更冻结 + 平台备份触发与台账）。
// 2026-10-26 批2 写路径命令化：五写位（mint/revoke/freeze/lift/backup）全
// 走 kit/settings.ts 的 resource commands；批4 读面收尾：读面 catalog 钩子
// 全量换 kit 资源（whoami 走 session record、tokens/freezes/platform-backups
// 走 kit 集合、roles 走 identity 同资源）——伴读固定窗不经 URL，轮询拍频
// 对齐旧 catalog 钩子（tokens 15s / freezes 30s / roles·backups 60s）。
export const Route = createFileRoute("/_shell/settings")({
  component: SettingsPageV2,
});

// 伴读固定窗：settings 三表原为整表渲染（无分页/快筛 UI），size 100 是
// clientCollection 取数上限的既定口径（projects=100）。
const allRowsQuery: CollectionQuery = { window: { size: 100 } };

function SettingsPageV2() {
  return (
    <PageFrame width="narrow" title="Settings" description="Identity, API tokens and platform governance">
      <div className="flex flex-col gap-5">
        <IdentityCard />
        <TokensCard />
        <GovernanceCard />
      </div>
    </PageFrame>
  );
}

function IdentityCard() {
  // 会话读（批4 kit 资源化）：与壳层同 record 键（["record","session","self"]）。
  const whoami = useRecord(sessionResource, { id: "self", query: { staleTime: 30_000 } });
  return (
    <SectionCard title="Identity" icon={KeyRound}>
      {whoami.status === "loading" ? (
        <Skeleton className="h-12 w-full" />
      ) : whoami.status === "error" ? (
        <ErrorState error={whoami.error} onRetry={() => void whoami.refetch()} />
      ) : (
        <dl className="grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-1.5 text-sm">
          <dt className="text-muted">token</dt>
          <dd className="font-medium">{whoami.data?.tokenName || "—"}</dd>
          <dt className="text-muted">user</dt>
          <dd className="font-medium">{whoami.data?.userName || "—"}</dd>
          <dt className="text-muted">role</dt>
          <dd>
            <StatusBadge tone="info">{whoami.data?.roleName || "—"}</StatusBadge>
          </dd>
        </dl>
      )}
    </SectionCard>
  );
}

function TokensCard() {
  // 台账读（批4 kit 资源化）：tokens 15s 轮询对齐旧 catalog useTokens
  //（RESOURCE_MS）；roles 60s 对齐旧 catalog useRoles（CATALOG_MS）——
  // roles 消费 identity 的 kit 集合（identity 页列表 15s 是另一窗形键）。
  const tokens = useQuery(
    collectionQueryOptions(tokensResource, allRowsQuery, { refetchInterval: 15_000 }),
  );
  const roles = useQuery(
    collectionQueryOptions(rolesResource, allRowsQuery, { refetchInterval: 60_000 }),
  );
  const tokenRows = tokens.data?.rows ?? [];
  const [createOpen, setCreateOpen] = useState(false);
  return (
    <SectionCard
      title="API tokens"
      icon={KeyRound}
      action={
        <Button size="sm" variant="outline" onClick={() => setCreateOpen(true)}>
          <PlusIcon data-icon-start-inline />
          New token…
        </Button>
      }
    >
      {tokens.isPending ? (
        <Skeleton className="h-16 w-full" />
      ) : tokens.isError ? (
        <ErrorState error={tokens.error} onRetry={() => void tokens.refetch()} />
      ) : tokenRows.length === 0 ? (
        <EmptyState icon={KeyRound} title="No tokens visible to this role" description="Token administration needs elevated scope." />
      ) : (
        <Table className="text-sm">
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Created</TableHead>
              <TableHead>Last used</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {tokenRows.map((token) => (
              <TokenRow key={token.id} token={token} />
            ))}
          </TableBody>
        </Table>
      )}
      <CreateTokenDialog open={createOpen} onClose={() => setCreateOpen(false)} roles={roles.data?.rows ?? []} />
    </SectionCard>
  );
}

function TokenRow({ token }: { token: { id?: string; name?: string; role_id?: string; created_at?: string; last_used_at?: string; revoked_at?: string } }) {
  const [revoking, setRevoking] = useState(false);
  const revoke = useCommand(tokensResource.commands.revoke, {
    onSettled: toastOnSettled(() => {
      toast("Token revoked — clients using it fail immediately");
      setRevoking(false);
    }),
  });
  return (
    <>
      <TableRow className={token.revoked_at ? "opacity-50" : ""}>
        <TableCell className="text-sm font-medium">
          {token.name}
          {token.revoked_at ? <span className="ml-2 text-xs text-muted">revoked</span> : null}
        </TableCell>
        <TableCell className="font-mono text-2xs text-muted">{token.role_id?.slice(0, 10)}…</TableCell>
        <TableCell className="text-xs text-muted"><RelativeTime value={token.created_at} /></TableCell>
        <TableCell className="text-xs text-muted"><RelativeTime value={token.last_used_at} /></TableCell>
        <TableCell className="text-right">
          {!token.revoked_at ? (
            <Button variant="outline" size="sm" className="text-[var(--status-danger)]" onClick={() => setRevoking(true)}>
              revoke
            </Button>
          ) : null}
        </TableCell>
      </TableRow>
      <AlertDialog open={revoking} onOpenChange={setRevoking}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Revoke token {token.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Clients using it fail immediately — this includes the console itself if it is the current token.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction asChild>
              <Button variant="destructive" disabled={revoke.pending} onClick={() => revoke.mutate({ id: token.id ?? "" })}>
                {revoke.pending ? "Revoking…" : "Revoke token"}
              </Button>
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function CreateTokenDialog({ open, onClose, roles }: { open: boolean; onClose: () => void; roles: ReadonlyArray<{ id?: string; name?: string; builtin?: boolean }> }) {
  const [name, setName] = useState("");
  const [roleId, setRoleId] = useState("");
  const [minted, setMinted] = useState<string | null>(null);
  // mint→reveal 揭示链：ok 进一次性 secret 揭示卡（无 toast——旧口径），
  // rejected/conflict 兜 toast.error(message)。
  const create = useCommand(tokensResource.commands.mint, {
    onSettled: toastOnSettled((payload) => setMinted(payload.secret ?? "secret not returned")),
  });
  return (
    <Dialog
      open={open}
      onOpenChange={(open) => {
        if (!open) {
          setMinted(null);
          setName("");
          onClose();
        }
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New API token</DialogTitle>
          <DialogDescription>The secret is shown once at creation — store it now.</DialogDescription>
        </DialogHeader>
        {minted != null ? (
          <div className="flex flex-col gap-3">
            <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-warning)_35%,transparent)] bg-[var(--status-warning-bg)] px-3 py-2 text-xs font-semibold text-[var(--status-warning)]">
              Secret shown once — copy it now:
            </div>
            <div className="relative">
              <pre className="overflow-x-auto rounded-lg border bg-panel-2/40 px-3 py-2.5 pr-9 font-mono text-xs break-all whitespace-pre-wrap text-[var(--status-success)]">{minted}</pre>
              <CopyButton value={minted} className="absolute top-2 right-2" />
            </div>
          </div>
        ) : (
          <form
            className="flex flex-col gap-3"
            onSubmit={(event) => {
              event.preventDefault();
              create.mutate({ name, roleId: roleId || roles[0]?.id });
            }}
          >
            <div className="flex flex-col gap-1.5">
              <Label className="text-xs">Name</Label>
              <Input value={name} onChange={(event) => setName(event.target.value)} autoFocus />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label className="text-xs">Role</Label>
              <Select value={roleId || roles[0]?.id || ""} onValueChange={setRoleId}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  {roles.map((role) => (
                    <SelectItem key={role.id} value={role.id ?? ""}>
                      {role.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </form>
        )}
        <DialogFooter>
          {minted != null ? (
            <Button onClick={() => { setMinted(null); setName(""); onClose(); }}>Done</Button>
          ) : (
            <>
              <Button variant="outline" onClick={onClose}>Cancel</Button>
              <Button disabled={create.pending || name === ""} onClick={() => create.mutate({ name, roleId: roleId || roles[0]?.id })}>
                {create.pending ? "Creating…" : "Create"}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function GovernanceCard() {
  // 冻结台账读（批4 kit 资源化）：30s 轮询对齐旧页内 useQuery 原值。
  const freezes = useQuery(
    collectionQueryOptions(freezesResource, allRowsQuery, { refetchInterval: 30_000 }),
  );
  const freezeRows = freezes.data?.rows ?? [];
  const [reason, setReason] = useState("");
  const [lifting, setLifting] = useState<{ id: string; reason: string } | null>(null);
  const freeze = useCommand(freezesResource.commands.freeze, {
    onSettled: toastOnSettled(() => {
      toast("Change freeze set — change verbs are refused until lifted");
      setReason("");
    }),
  });
  const backup = useCommand(platformBackupsResource.commands.trigger, {
    onSettled: toastOnSettled((payload) => toast(`Platform backup ${payload.backup?.id ?? ""} finished`)),
  });
  const lift = useCommand(freezesResource.commands.lift, {
    onSettled: toastOnSettled(() => {
      toast("Change freeze lifted");
      setLifting(null);
    }),
  });
  const active = freezeRows.filter((row) => !row.lifted_at);

  return (
    <SectionCard title="Platform governance" icon={ShieldOffIcon}>
      <div className="flex flex-col gap-4">
        {active.length > 0 ? (
          <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-warning)_35%,transparent)] bg-[var(--status-warning-bg)] px-3 py-2.5">
            <div className="text-xs font-semibold text-[var(--status-warning)]">change freeze active</div>
            <ul className="mt-1.5 flex flex-col gap-1.5">
              {active.map((row) => (
                <li key={row.id} className="flex items-center justify-between gap-3 text-xs">
                  <span>
                    {row.reason} · <RelativeTime value={row.created_at} />
                    <span className="ml-2 font-mono opacity-60" title={row.id}>
                      {row.id?.slice(0, 10)}…
                    </span>
                  </span>
                  <Button variant="outline" size="sm" onClick={() => setLifting({ id: row.id ?? "", reason: row.reason ?? "" })}>
                    lift
                  </Button>
                </li>
              ))}
            </ul>
          </div>
        ) : null}
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            freeze.mutate({ reason });
          }}
        >
          <div className="flex flex-col gap-1">
            <Label className="text-xs">Set change freeze</Label>
            <Input value={reason} onChange={(event) => setReason(event.target.value)} placeholder="maintenance window" className="h-8 w-56" />
            <p className="text-2xs text-muted">platform-wide (all teams); change verbs are refused until lifted</p>
          </div>
          <Button type="submit" size="sm" disabled={freeze.pending || reason === ""} className="mb-4.5">
            set freeze
          </Button>
        </form>
        <div className="flex flex-wrap items-center gap-3 border-t pt-3">
          <Button variant="outline" size="sm" disabled={backup.pending} onClick={() => backup.mutate(undefined)}>
            <DatabaseBackupIcon data-icon-start-inline />
            {backup.pending ? "Backing up…" : "Trigger platform backup"}
          </Button>
          <span className="text-xs text-muted">synchronous full platform snapshot (SQLite + secrets + object store)</span>
        </div>
        <PlatformBackupsTable />
      </div>

      <AlertDialog open={lifting != null} onOpenChange={(open) => !open && setLifting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Lift the change freeze "{lifting?.reason}"?</AlertDialogTitle>
            <AlertDialogDescription>Change verbs are accepted again immediately.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction asChild>
              <Button disabled={lift.pending} onClick={() => lifting && lift.mutate({ id: lifting.id })}>
                {lift.pending ? "Lifting…" : "Lift freeze"}
              </Button>
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SectionCard>
  );
}

// PlatformBackupsTable 是平台备份台账（C2 补齐面）：只读消费——触发按钮
// 写后即时失效（kit 集合键默认失效）；pre-upgrade 快照是换装硬门（runbook
// 换装序）。60s 轮询对齐旧 catalog usePlatformBackups。
function PlatformBackupsTable() {
  const backups = useQuery(
    collectionQueryOptions(platformBackupsResource, allRowsQuery, { refetchInterval: 60_000 }),
  );
  const backupRows = backups.data?.rows ?? [];
  if (backups.isPending) return <Skeleton className="h-16 w-full" />;
  if (backups.isError) return <ErrorState error={backups.error} onRetry={() => void backups.refetch()} />;
  if (backupRows.length === 0) {
    return (
      <EmptyState
        icon={DatabaseBackupIcon}
        title="No platform backups yet"
        description="Trigger one above — a pre-upgrade snapshot is the hard gate for binary swaps."
      />
    );
  }
  return (
    <Table className="text-sm">
      <TableHeader>
        <TableRow>
          <TableHead>Snapshot</TableHead>
          <TableHead>Time</TableHead>
          <TableHead>Hostname</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {backupRows.map((row) => (
          <TableRow key={row.id}>
            <TableCell className="font-mono text-xs">
              {row.id}
              <CopyButton value={row.id ?? ""} className="ml-1" />
            </TableCell>
            <TableCell className="text-xs text-muted" title={row.time}>
              <RelativeTime value={row.time} />
            </TableCell>
            <TableCell className="text-xs text-muted">{row.hostname}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
