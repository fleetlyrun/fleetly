import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { DatabaseBackupIcon, KeyRound, PlusIcon, ShieldOffIcon } from "lucide-react";
import { toast } from "sonner";
import { apiFetch, apiSend } from "@/api/client";
import { usePlatformBackups, useRoles, useTokens, useWhoami } from "@/lib/catalog";
import { describeError } from "@/lib/api-errors";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { ErrorState } from "@/components/domain/error-state";
import { PageHeader } from "@/components/domain/page-header";
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

// 设置页（F3.1 → UI v2 批 5 reskin）：身份（whoami）/ Token 铸造与吊销
//（Console 自身凭证生命周期；secret 一次性揭示）/ 平台治理（变更冻结 +
// 平台备份触发与台账）。全部动词语义原样，仅换呈现。
export const Route = createFileRoute("/_shell/settings")({
  component: SettingsPageV2,
});

function SettingsPageV2() {
  return (
    <div className="mx-auto max-w-5xl px-6 pt-6 pb-8">
      <PageHeader title="Settings" description="Identity, API tokens and platform governance" />
      <div className="flex flex-col gap-5">
        <IdentityCard />
        <TokensCard />
        <GovernanceCard />
      </div>
    </div>
  );
}

function Card({ title, icon: Icon, children, action }: { title: string; icon: React.ComponentType<{ className?: string }>; children: React.ReactNode; action?: React.ReactNode }) {
  return (
    <section className="rounded-xl border bg-card">
      <div className="flex items-center gap-2 border-b px-4 py-3">
        <Icon className="size-4 text-muted-foreground" />
        <h2 className="text-[13.5px] font-semibold">{title}</h2>
        {action ? <div className="ml-auto">{action}</div> : null}
      </div>
      <div className="px-4 py-3.5">{children}</div>
    </section>
  );
}

function IdentityCard() {
  const whoami = useWhoami(true);
  return (
    <Card title="Identity" icon={KeyRound}>
      {whoami.isPending ? (
        <Skeleton className="h-12 w-full" />
      ) : whoami.isError ? (
        <ErrorState error={whoami.error} onRetry={() => void whoami.refetch()} />
      ) : (
        <dl className="grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-1.5 text-[13px]">
          <dt className="text-muted-foreground">token</dt>
          <dd className="font-medium">{whoami.data.tokenName || "—"}</dd>
          <dt className="text-muted-foreground">user</dt>
          <dd className="font-medium">{whoami.data.userName || "—"}</dd>
          <dt className="text-muted-foreground">role</dt>
          <dd>
            <StatusBadge tone="info">{whoami.data.roleName || "—"}</StatusBadge>
          </dd>
        </dl>
      )}
    </Card>
  );
}

function TokensCard() {
  const tokens = useTokens();
  const roles = useRoles();
  const [createOpen, setCreateOpen] = useState(false);
  return (
    <Card
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
      ) : (tokens.data ?? []).length === 0 ? (
        <EmptyState icon={KeyRound} title="No tokens visible to this role" description="Token administration needs elevated scope." />
      ) : (
        <Table className="text-[13px]">
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
            {(tokens.data ?? []).map((token) => (
              <TokenRow key={token.id} token={token} />
            ))}
          </TableBody>
        </Table>
      )}
      <CreateTokenDialog open={createOpen} onClose={() => setCreateOpen(false)} roles={roles.data ?? []} />
    </Card>
  );
}

function TokenRow({ token }: { token: { id?: string; name?: string; role_id?: string; created_at?: string; last_used_at?: string; revoked_at?: string } }) {
  const queryClient = useQueryClient2();
  const [revoking, setRevoking] = useState(false);
  const revoke = useMutation({
    mutationFn: () => apiSend(`/v1/tokens/${encodeURIComponent(token.id ?? "")}/revoke`, "POST"),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["settings", "tokens"] });
      toast("Token revoked — clients using it fail immediately");
      setRevoking(false);
    },
    onError: (cause) => toast.error(`${describeError(cause).title} — ${describeError(cause).detail}`),
  });
  return (
    <>
      <TableRow className={token.revoked_at ? "opacity-50" : ""}>
        <TableCell className="text-[13px] font-medium">
          {token.name}
          {token.revoked_at ? <span className="ml-2 text-xs text-muted-foreground">revoked</span> : null}
        </TableCell>
        <TableCell className="font-mono text-[11px] text-muted-foreground">{token.role_id?.slice(0, 10)}…</TableCell>
        <TableCell className="text-xs text-muted-foreground"><RelativeTime value={token.created_at} /></TableCell>
        <TableCell className="text-xs text-muted-foreground"><RelativeTime value={token.last_used_at} /></TableCell>
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
              <Button variant="destructive" disabled={revoke.isPending} onClick={() => revoke.mutate()}>
                {revoke.isPending ? "Revoking…" : "Revoke token"}
              </Button>
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

// useQueryClient2 避免与本文件顶层导入重名（薄别名）。
import { useQueryClient as useQueryClient2 } from "@tanstack/react-query";

function CreateTokenDialog({ open, onClose, roles }: { open: boolean; onClose: () => void; roles: Array<{ id?: string; name?: string; builtin?: boolean }> }) {
  const [name, setName] = useState("");
  const [roleId, setRoleId] = useState("");
  const [minted, setMinted] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () =>
      apiSend<{ token?: { id?: string }; secret?: string }>("/v1/tokens", "POST", {
        name,
        role_id: roleId || roles[0]?.id,
      }),
    onSuccess: (response) => {
      setMinted(response.secret ?? "secret not returned");
    },
    onError: (cause) => toast.error(`${describeError(cause).title} — ${describeError(cause).detail}`),
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
              <pre className="overflow-x-auto rounded-lg border bg-muted/40 px-3 py-2.5 pr-9 font-mono text-xs break-all whitespace-pre-wrap text-[var(--status-success)]">{minted}</pre>
              <CopyButton value={minted} className="absolute top-2 right-2" />
            </div>
          </div>
        ) : (
          <form className="flex flex-col gap-3" onSubmit={(event) => { event.preventDefault(); create.mutate(); }}>
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
              <Button disabled={create.isPending || name === ""} onClick={() => create.mutate()}>
                {create.isPending ? "Creating…" : "Create"}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function GovernanceCard() {
  const freezes = useQuery({
    queryKey: ["settings", "freezes"],
    queryFn: async () => {
      const res = await apiFetch<{ freezes?: Array<{ id?: string; team_id?: string; reason?: string; created_at?: string; lifted_at?: string } | undefined> }>("/v1/governance/freezes");
      return (res.freezes ?? []).flatMap((row) => (row != null ? [row] : []));
    },
    refetchInterval: 30_000,
  });
  const queryClient = useQueryClient2();
  const [reason, setReason] = useState("");
  const [lifting, setLifting] = useState<{ id: string; reason: string } | null>(null);
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["settings", "freezes"] });
  const freeze = useMutation({
    mutationFn: () => apiSend("/v1/governance/freezes", "POST", { reason }),
    onSuccess: () => {
      toast("Change freeze set — change verbs are refused until lifted");
      invalidate();
      setReason("");
    },
    onError: (cause) => toast.error(`${describeError(cause).title} — ${describeError(cause).detail}`),
  });
  const backup = useMutation({
    mutationFn: () => apiSend<{ backup?: { id?: string } }>("/v1/platform/backups", "POST", {}),
    onSuccess: (data) => {
      toast(`Platform backup ${data.backup?.id ?? ""} finished`);
      void queryClient.invalidateQueries({ queryKey: ["settings", "platform-backups"] });
    },
    onError: (cause) => toast.error(`${describeError(cause).title} — ${describeError(cause).detail}`),
  });
  const lift = useMutation({
    mutationFn: (id: string) => apiSend(`/v1/governance/freezes/${encodeURIComponent(id)}/lift`, "POST"),
    onSuccess: () => {
      toast("Change freeze lifted");
      invalidate();
      setLifting(null);
    },
    onError: (cause) => toast.error(`${describeError(cause).title} — ${describeError(cause).detail}`),
  });
  const active = (freezes.data ?? []).filter((row) => !row.lifted_at);

  return (
    <Card title="Platform governance" icon={ShieldOffIcon}>
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
            freeze.mutate();
          }}
        >
          <div className="flex flex-col gap-1">
            <Label className="text-xs">Set change freeze</Label>
            <Input value={reason} onChange={(event) => setReason(event.target.value)} placeholder="maintenance window" className="h-8 w-56" />
            <p className="text-[11px] text-muted-foreground">platform-wide (all teams); change verbs are refused until lifted</p>
          </div>
          <Button type="submit" size="sm" disabled={freeze.isPending || reason === ""} className="mb-4.5">
            set freeze
          </Button>
        </form>
        <div className="flex flex-wrap items-center gap-3 border-t pt-3">
          <Button variant="outline" size="sm" disabled={backup.isPending} onClick={() => backup.mutate()}>
            <DatabaseBackupIcon data-icon-start-inline />
            {backup.isPending ? "Backing up…" : "Trigger platform backup"}
          </Button>
          <span className="text-xs text-muted-foreground">synchronous full platform snapshot (SQLite + secrets + object store)</span>
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
              <Button disabled={lift.isPending} onClick={() => lifting && lift.mutate(lifting.id)}>
                {lift.isPending ? "Lifting…" : "Lift freeze"}
              </Button>
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  );
}

// PlatformBackupsTable 是平台备份台账（C2 补齐面）：只读消费——触发按钮
// 写后即时失效；pre-upgrade 快照是换装硬门（runbook 换装序）。
function PlatformBackupsTable() {
  const backups = usePlatformBackups();
  if (backups.isPending) return <Skeleton className="h-16 w-full" />;
  if (backups.isError) return <ErrorState error={backups.error} onRetry={() => void backups.refetch()} />;
  if ((backups.data ?? []).length === 0) {
    return (
      <EmptyState
        icon={DatabaseBackupIcon}
        title="No platform backups yet"
        description="Trigger one above — a pre-upgrade snapshot is the hard gate for binary swaps."
      />
    );
  }
  return (
    <Table className="text-[13px]">
      <TableHeader>
        <TableRow>
          <TableHead>Snapshot</TableHead>
          <TableHead>Time</TableHead>
          <TableHead>Hostname</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {(backups.data ?? []).map((row) => (
          <TableRow key={row.id}>
            <TableCell className="font-mono text-xs">
              {row.id}
              <CopyButton value={row.id ?? ""} className="ml-1" />
            </TableCell>
            <TableCell className="text-xs text-muted-foreground" title={row.time}>
              <RelativeTime value={row.time} />
            </TableCell>
            <TableCell className="text-xs text-muted-foreground">{row.hostname}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
