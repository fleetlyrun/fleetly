import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { PlusIcon } from "lucide-react";
import { toast } from "sonner";
import type { components as identitySchemas } from "@/api/identity";
import { apiSend } from "@/api/client";
import { describeError } from "@/lib/api-errors";
import { useInvitations, useRoles, useTeams, useUsers } from "@/lib/catalog";
import { CopyButton } from "@/components/domain/copy-button";
import { CliEquivalent, ListPagination, ListToolbar, useClientPage, useListFilter } from "@/components/domain/list-toolbar";
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
import { PageTabs } from "@/components/domain/page-tabs";

type User = identitySchemas["schemas"]["v1User"];
type Team = identitySchemas["schemas"]["v1Team"];
type Role = identitySchemas["schemas"]["v1Role"];
type Invitation = identitySchemas["schemas"]["v1Invitation"];

// 身份管理页（C2 治理批 → UI v2 批 5 reskin）：RBAC 的 UI 面——users /
// teams / roles / invitations 四 tab（URL param），动词面对齐 CLI。明文
// 凭证沿 CLI 口径：邀请 secret 只在铸造响应出现一次（SecretReveal 形态）。
const identitySearch = z.object({ tab: z.enum(["users", "teams", "roles", "invitations"]).optional() });

export const Route = createFileRoute("/_shell/identity")({
  validateSearch: identitySearch,
  component: IdentityPageV2,
});

function IdentityPageV2() {
  const navigate = Route.useNavigate();
  const tab = Route.useSearch({ select: (search) => search.tab ?? "users" });
  // 创建入口在各 tab 工具栏行右侧（对齐批 5 用户复裁：页头钮位退役）。
  // 切 tab 即收起未完成的创建弹窗。
  const [createOpen, setCreateOpen] = useState(false);
  return (
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader
        title="Identity"
        description="Users, teams, roles and invitations — the RBAC face"
      />
      <PageTabs
        tabs={[
          { value: "users", label: "Users" },
          { value: "teams", label: "Teams" },
          { value: "roles", label: "Roles" },
          { value: "invitations", label: "Invitations" },
        ]}
        current={tab}
        onChange={(value) => {
          setCreateOpen(false);
          void navigate({ search: { tab: value } });
        }}
      />
      {tab === "users" ? <UsersTab createOpen={createOpen} onCreateOpenChange={setCreateOpen} /> : null}
      {tab === "teams" ? <TeamsTab /> : null}
      {tab === "roles" ? <RolesTab createOpen={createOpen} onCreateOpenChange={setCreateOpen} /> : null}
      {tab === "invitations" ? <InvitationsTab createOpen={createOpen} onCreateOpenChange={setCreateOpen} /> : null}
    </div>
  );
}

// ---- 共用小件 ----

function PanelCard({ children }: { children: React.ReactNode }) {
  return <div className="rounded-xl border bg-card">{children}</div>;
}

function useInvalidate(key: readonly unknown[]) {
  const queryClient = useQueryClient();
  return () => void queryClient.invalidateQueries({ queryKey: [...key] });
}

function fieldError(error: unknown): string {
  return `${describeError(error).title} — ${describeError(error).detail}`;
}

// ---- users ----

function UsersTab({ createOpen, onCreateOpenChange }: { createOpen: boolean; onCreateOpenChange: (open: boolean) => void }) {
  const users = useUsers();
  const [query, setQuery] = useState("");
  const filtered = useListFilter(users.data ?? [], query, (user: User) => [user.name ?? "", user.id ?? ""]);
  const { page, pageCount, pageRows, setPage } = useClientPage(filtered);
  return (
    <>
      <PanelCard>
        <div className="px-3 pt-3">
          <ListToolbar
            label="users"
            value={query}
            onChange={setQuery}
            placeholder="Filter users..."
            total={(users.data ?? []).length}
            shown={filtered.length}
            actions={
              <Button size="sm" onClick={() => onCreateOpenChange(true)}>
                <PlusIcon data-icon-start-inline />
                New user…
              </Button>
            }
          />
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>ID</TableHead>
              <TableHead>Created</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {users.isPending ? (
              <TableRow>
                <TableCell colSpan={4}><Skeleton className="h-5 w-full" /></TableCell>
              </TableRow>
            ) : users.isError ? (
              <TableRow>
                <TableCell colSpan={4}><ErrorState error={users.error} onRetry={() => void users.refetch()} /></TableCell>
              </TableRow>
            ) : (users.data ?? []).length === 0 ? (
              <TableRow>
                <TableCell colSpan={4}>
                  <EmptyState icon={PlusIcon} title="No users yet" description="Create one, or mint a token in Settings." />
                </TableCell>
              </TableRow>
            ) : (
              pageRows.map((user) => <UserRow key={user.id} user={user} />)
            )}
          </TableBody>
        </Table>
        <ListPagination page={page} pageCount={pageCount} setPage={setPage} total={filtered.length} />
      </PanelCard>
      <CliEquivalent command="fleetly users list" />
      <CreateUserDialog open={createOpen} onClose={() => onCreateOpenChange(false)} />
    </>
  );
}

function UserRow({ user }: { user: User }) {
  const invalidate = useInvalidate(["identity", "users"]);
  const [deleting, setDeleting] = useState(false);
  const [pwOpen, setPwOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => apiSend(`/v1/users/${encodeURIComponent(user.id ?? "")}`, "DELETE"),
    onSuccess: () => {
      toast("User deleted");
      invalidate();
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  return (
    <>
      <TableRow>
        <TableCell className="text-[13px] font-medium">{user.name}</TableCell>
        <TableCell className="font-mono text-[11px] text-muted-foreground">
          {user.id?.slice(0, 10)}…<CopyButton value={user.id ?? ""} />
        </TableCell>
        <TableCell className="text-xs text-muted-foreground"><RelativeTime value={user.created_at} /></TableCell>
        <TableCell className="text-right">
          <div className="flex items-center justify-end gap-1.5">
            <Button variant="outline" size="sm" onClick={() => setPwOpen((prev) => !prev)}>
              password…
            </Button>
            <Button variant="outline" size="sm" className="text-[var(--status-danger)]" onClick={() => setDeleting(true)}>
              delete
            </Button>
          </div>
        </TableCell>
      </TableRow>
      {pwOpen ? <SetPasswordRow userId={user.id ?? ""} onDone={() => setPwOpen(false)} /> : null}
      <AlertDialogLike
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete user ${user.name}?`}
        description="Tokens minted by this user lose their membership scoping. This cannot be undone."
        confirmLabel="Delete user"
        onConfirm={() => remove.mutateAsync().catch(() => undefined)}
      />
    </>
  );
}

// SetPasswordRow 是行内设密表单（admin 重置面；C6 第一期——自助改密随
// SSO 批裁决）。POST /v1/users/{id}/password，users:write scope 执法。
function SetPasswordRow({ userId, onDone }: { userId: string; onDone: () => void }) {
  const [password, setPassword] = useState("");
  const [pending, setPending] = useState(false);
  return (
    <TableRow className="hover:bg-transparent">
      <TableCell colSpan={4} className="bg-muted/30">
        <form
          className="flex flex-wrap items-end gap-2 py-1"
          onSubmit={(event) => {
            event.preventDefault();
            setPending(true);
            apiSend(`/v1/users/${encodeURIComponent(userId)}/password`, "POST", { password })
              .then(() => {
                toast("Password set");
                setPassword("");
                onDone();
              })
              .catch((cause: unknown) => toast.error(fieldError(cause)))
              .finally(() => setPending(false));
          }}
        >
          <div className="flex flex-col gap-1">
            <Label className="text-xs">New password</Label>
            <Input type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="••••••••" autoFocus className="h-8 w-48" />
          </div>
          <Button type="submit" size="sm" disabled={pending || password === ""} className="mb-0.5">
            {pending ? "Setting…" : "Set password"}
          </Button>
          <p className="mb-1.5 text-[11px] text-muted-foreground">admin reset — the user signs in with this on the password tab</p>
        </form>
      </TableCell>
    </TableRow>
  );
}

function CreateUserDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const teams = useTeams();
  const roles = useRoles();
  const invalidate = useInvalidate(["identity", "users"]);
  const [name, setName] = useState("");
  const [teamId, setTeamId] = useState("");
  const [roleId, setRoleId] = useState("");
  const [password, setPassword] = useState("");
  const create = useMutation({
    mutationFn: () =>
      apiSend("/v1/users", "POST", {
        name,
        team_id: teamId === "" ? undefined : teamId,
        role_id: roleId === "" ? undefined : roleId,
        password: password === "" ? undefined : password,
      }),
    onSuccess: () => {
      toast("User created");
      invalidate();
      onClose();
      setName("");
      setPassword("");
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  return (
    <Dialog open={open} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New user</DialogTitle>
        </DialogHeader>
        <form className="flex flex-col gap-3" onSubmit={(event) => { event.preventDefault(); create.mutate(); }}>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Name</Label>
            <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="alice" autoFocus required />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Team</Label>
            <Select value={teamId} onValueChange={setTeamId}>
              <SelectTrigger><SelectValue placeholder="default" /></SelectTrigger>
              <SelectContent>
                {(teams.data ?? []).map((team) => (
                  <SelectItem key={team.id} value={team.id ?? ""}>{team.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-[11px] text-muted-foreground">defaults to the default team</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Role</Label>
            <Select value={roleId} onValueChange={setRoleId}>
              <SelectTrigger><SelectValue placeholder="— pick a role —" /></SelectTrigger>
              <SelectContent>
                {(roles.data ?? []).map((role) => (
                  <SelectItem key={role.id} value={role.id ?? ""}>{role.name}{role.builtin ? " (builtin)" : ""}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-[11px] text-muted-foreground">the role granted within the team</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Initial password (optional)</Label>
            <Input type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="••••••••" />
            <p className="text-[11px] text-muted-foreground">empty means no password login (set one later from the users row)</p>
          </div>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button disabled={create.isPending || name === ""} onClick={() => create.mutate()}>
            {create.isPending ? "Creating…" : "Create user"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- teams ----

function TeamsTab() {
  const teams = useTeams();
  const invalidate = useInvalidate(["identity", "teams"]);
  const [name, setName] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [deleting, setDeleting] = useState<Team | null>(null);
  const [query, setQuery] = useState("");
  const filtered = useListFilter(teams.data ?? [], query, (team: Team) => [team.name ?? "", team.id ?? ""]);
  const { page, pageCount, pageRows, setPage } = useClientPage(filtered);
  const create = useMutation({
    mutationFn: () => apiSend("/v1/teams", "POST", { name }),
    onSuccess: () => {
      toast("Team created");
      invalidate();
      setName("");
      setCreateOpen(false);
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  return (
    <>
      <PanelCard>
        <div className="px-3 pt-3">
          <ListToolbar
            label="teams"
            value={query}
            onChange={setQuery}
            placeholder="Filter teams..."
            total={(teams.data ?? []).length}
            shown={filtered.length}
            actions={
              <Button size="sm" onClick={() => { setName(""); setCreateOpen(true); }}>
                <PlusIcon data-icon-start-inline />
                New team…
              </Button>
            }
          />
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>ID</TableHead>
              <TableHead>Created</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {teams.isPending ? (
              <TableRow><TableCell colSpan={4}><Skeleton className="h-5 w-full" /></TableCell></TableRow>
            ) : teams.isError ? (
              <TableRow><TableCell colSpan={4}><ErrorState error={teams.error} onRetry={() => void teams.refetch()} /></TableCell></TableRow>
            ) : (teams.data ?? []).length === 0 ? (
              <TableRow>
                <TableCell colSpan={4}>
                  <EmptyState icon={PlusIcon} title="No teams" description="The default team always exists implicitly." />
                </TableCell>
              </TableRow>
            ) : (
              pageRows.map((team) => (
                <TableRow key={team.id}>
                  <TableCell className="text-[13px] font-medium">{team.name}</TableCell>
                  <TableCell className="font-mono text-[11px] text-muted-foreground">
                    {team.id?.slice(0, 10)}…<CopyButton value={team.id ?? ""} />
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground"><RelativeTime value={team.created_at} /></TableCell>
                  <TableCell className="text-right">
                    <Button variant="outline" size="sm" className="text-[var(--status-danger)]" onClick={() => setDeleting(team)}>
                      delete
                    </Button>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
        <ListPagination page={page} pageCount={pageCount} setPage={setPage} total={filtered.length} />
      </PanelCard>
      <CliEquivalent command="fleetly teams list" />
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>New team</DialogTitle>
          </DialogHeader>
          <form
            className="flex flex-col gap-3"
            onSubmit={(event) => {
              event.preventDefault();
              create.mutate();
            }}
          >
            <div className="flex flex-col gap-1.5">
              <Label className="text-xs">Name</Label>
              <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="platform" autoFocus required />
            </div>
          </form>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button disabled={create.isPending || name === ""} onClick={() => create.mutate()}>
              {create.isPending ? "Creating…" : "Create team"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <AlertDialogLike
        open={deleting != null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        title={`Delete team ${deleting?.name ?? ""}?`}
        description="Members lose this team's role grants. This cannot be undone."
        confirmLabel="Delete team"
        onConfirm={() =>
          apiSend(`/v1/teams/${encodeURIComponent(deleting?.id ?? "")}`, "DELETE")
            .then(() => {
              invalidate();
              toast("Team deleted");
            })
        }
      />
    </>
  );
}

// ---- roles ----

function RolesTab({ createOpen, onCreateOpenChange }: { createOpen: boolean; onCreateOpenChange: (open: boolean) => void }) {
  const teams = useTeams();
  const roles = useRoles();
  const [query, setQuery] = useState("");
  const filtered = useListFilter(roles.data ?? [], query, (role: Role) => [role.name ?? "", ...(role.scopes ?? [])]);
  const { page, pageCount, pageRows, setPage } = useClientPage(filtered);
  return (
    <>
      <PanelCard>
        <div className="px-3 pt-3">
          <ListToolbar
            label="roles"
            value={query}
            onChange={setQuery}
            placeholder="Filter roles..."
            total={(roles.data ?? []).length}
            shown={filtered.length}
            actions={
              <Button size="sm" onClick={() => onCreateOpenChange(true)}>
                <PlusIcon data-icon-start-inline />
                New role…
              </Button>
            }
          />
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Team</TableHead>
              <TableHead>Kind</TableHead>
              <TableHead>Scopes</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {roles.isPending ? (
              <TableRow><TableCell colSpan={5}><Skeleton className="h-5 w-full" /></TableCell></TableRow>
            ) : roles.isError ? (
              <TableRow><TableCell colSpan={5}><ErrorState error={roles.error} onRetry={() => void roles.refetch()} /></TableCell></TableRow>
            ) : (roles.data ?? []).length === 0 ? (
              <TableRow>
                <TableCell colSpan={5}>
                  <EmptyState icon={PlusIcon} title="No roles" description="Builtin templates are listed here once queried per team." />
                </TableCell>
              </TableRow>
            ) : (
              pageRows.map((role) => <RoleRow key={role.id} role={role} />)
            )}
          </TableBody>
        </Table>
        <ListPagination page={page} pageCount={pageCount} setPage={setPage} total={filtered.length} />
      </PanelCard>
      <CliEquivalent command="fleetly roles list" />
      <CreateRoleDialog open={createOpen} onClose={() => onCreateOpenChange(false)} teams={teams.data ?? []} />
    </>
  );
}

function RoleRow({ role }: { role: Role }) {
  const invalidate = useInvalidate(["settings", "roles"]);
  const [deleting, setDeleting] = useState(false);
  return (
    <>
      <TableRow>
        <TableCell className="text-[13px] font-medium">{role.name}</TableCell>
        <TableCell className="font-mono text-[11px] text-muted-foreground">{role.team_id?.slice(0, 10) ?? "—"}</TableCell>
        <TableCell>
          <StatusBadge tone={role.builtin ? "info" : "neutral"}>{role.builtin ? "builtin" : "custom"}</StatusBadge>
        </TableCell>
        <TableCell className="max-w-md truncate font-mono text-[11px] text-muted-foreground" title={(role.scopes ?? []).join(" ")}>
          {(role.scopes ?? []).join(" ")}
        </TableCell>
        <TableCell className="text-right">
          {role.builtin ? null : (
            <Button variant="outline" size="sm" className="text-[var(--status-danger)]" onClick={() => setDeleting(true)}>
              delete
            </Button>
          )}
        </TableCell>
      </TableRow>
      <AlertDialogLike
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete role ${role.name}?`}
        description="Users holding this role lose its scopes. This cannot be undone."
        confirmLabel="Delete role"
        onConfirm={() =>
          apiSend(`/v1/roles/${encodeURIComponent(role.id ?? "")}`, "DELETE")
            .then(() => {
              invalidate();
              toast("Role deleted");
            })
        }
      />
    </>
  );
}

function CreateRoleDialog({ open, onClose, teams }: { open: boolean; onClose: () => void; teams: Team[] }) {
  const invalidate = useInvalidate(["settings", "roles"]);
  const [name, setName] = useState("");
  const [teamId, setTeamId] = useState("");
  const [scopes, setScopes] = useState("");
  const create = useMutation({
    mutationFn: () =>
      apiSend("/v1/roles", "POST", {
        name,
        team_id: teamId === "" ? undefined : teamId,
        scopes: scopes.split(/[\s,]+/).filter((part) => part !== ""),
      }),
    onSuccess: () => {
      toast("Role created");
      invalidate();
      onClose();
      setName("");
      setScopes("");
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  return (
    <Dialog open={open} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New role</DialogTitle>
          <DialogDescription>Scopes are space-separated resource:action pairs (write implies read at enforcement).</DialogDescription>
        </DialogHeader>
        <form className="flex flex-col gap-3" onSubmit={(event) => { event.preventDefault(); create.mutate(); }}>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Name</Label>
            <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="deployer" autoFocus required />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Team</Label>
            <Select value={teamId} onValueChange={setTeamId}>
              <SelectTrigger><SelectValue placeholder="— pick a team —" /></SelectTrigger>
              <SelectContent>
                {teams.map((team) => (
                  <SelectItem key={team.id} value={team.id ?? ""}>{team.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-[11px] text-muted-foreground">custom roles belong to a team</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Scopes</Label>
            <Input value={scopes} onChange={(event) => setScopes(event.target.value)} placeholder="apps:admin deployments:admin logs:read" spellCheck={false} required />
          </div>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button disabled={create.isPending || name === "" || teamId === ""} onClick={() => create.mutate()}>
            {create.isPending ? "Creating…" : "Create role"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- invitations ----

function InvitationsTab({ createOpen, onCreateOpenChange }: { createOpen: boolean; onCreateOpenChange: (open: boolean) => void }) {
  const invitations = useInvitations();
  const teams = useTeams();
  const roles = useRoles();
  const [query, setQuery] = useState("");
  const filtered = useListFilter(invitations.data ?? [], query, (invitation: Invitation) => [invitation.id ?? "", invitation.team_id ?? "", invitation.role_id ?? "", invitation.created_by ?? ""]);
  const { page, pageCount, pageRows, setPage } = useClientPage(filtered);
  return (
    <>
      <PanelCard>
        <div className="px-3 pt-3">
          <ListToolbar
            label="invitations"
            value={query}
            onChange={setQuery}
            placeholder="Filter invitations..."
            total={(invitations.data ?? []).length}
            shown={filtered.length}
            actions={
              <Button size="sm" onClick={() => onCreateOpenChange(true)}>
                <PlusIcon data-icon-start-inline />
                New invitation…
              </Button>
            }
          />
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>ID</TableHead>
              <TableHead>Team</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Created by</TableHead>
              <TableHead>Expires</TableHead>
              <TableHead>Consumed</TableHead>
              <TableHead>Created</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {invitations.isPending ? (
              <TableRow><TableCell colSpan={7}><Skeleton className="h-5 w-full" /></TableCell></TableRow>
            ) : invitations.isError ? (
              <TableRow><TableCell colSpan={7}><ErrorState error={invitations.error} onRetry={() => void invitations.refetch()} /></TableCell></TableRow>
            ) : (invitations.data ?? []).length === 0 ? (
              <TableRow>
                <TableCell colSpan={7}>
                  <EmptyState icon={PlusIcon} title="No invitations" description="Mint one to onboard a teammate — the secret is shown once." />
                </TableCell>
              </TableRow>
            ) : (
              pageRows.map((invitation) => (
                <TableRow key={invitation.id}>
                  <TableCell className="font-mono text-[11px] text-muted-foreground">
                    {invitation.id?.slice(0, 10)}…<CopyButton value={invitation.id ?? ""} />
                  </TableCell>
                  <TableCell className="font-mono text-[11px] text-muted-foreground">{invitation.team_id?.slice(0, 10)}</TableCell>
                  <TableCell className="font-mono text-[11px] text-muted-foreground">{invitation.role_id?.slice(0, 10)}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{invitation.created_by}</TableCell>
                  <TableCell className="text-xs text-muted-foreground"><RelativeTime value={invitation.expires_at} /></TableCell>
                  <TableCell>
                    {invitation.consumed_at ? (
                      <span className="text-xs text-muted-foreground">yes</span>
                    ) : (
                      <StatusBadge tone="success">open</StatusBadge>
                    )}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground"><RelativeTime value={invitation.created_at} /></TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
        <ListPagination page={page} pageCount={pageCount} setPage={setPage} total={filtered.length} />
      </PanelCard>
      {/* invitations 无 CLI list 动词——CLI 行诚实省略 */}
      <CreateInvitationDialog open={createOpen} onClose={() => onCreateOpenChange(false)} teams={teams.data ?? []} roles={roles.data ?? []} />
    </>
  );
}

function CreateInvitationDialog({ open, onClose, teams, roles }: { open: boolean; onClose: () => void; teams: Team[]; roles: Role[] }) {
  const invalidate = useInvalidate(["identity", "invitations"]);
  const [teamId, setTeamId] = useState("");
  const [roleId, setRoleId] = useState("");
  const [ttl, setTtl] = useState("24h");
  const [secret, setSecret] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () =>
      apiSend<{ invitation?: Invitation; secret?: string }>("/v1/invitations", "POST", {
        team_id: teamId === "" ? undefined : teamId,
        role_id: roleId === "" ? undefined : roleId,
        ttl: ttl === "" ? undefined : ttl,
      }),
    onSuccess: (data) => {
      invalidate();
      setSecret(data.secret ?? "");
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });

  // SecretReveal 形态：一次性 secret 专用揭示卡（复制 + 不可再现警告）。
  if (secret != null) {
    return (
      <Dialog open onOpenChange={(open) => { if (!open) { setSecret(null); onClose(); } }}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Invitation minted</DialogTitle>
            <DialogDescription>
              Accept with <span className="font-mono">fleetly users accept --token {"<secret>"} --name ALICE</span>. The secret is shown once.
            </DialogDescription>
          </DialogHeader>
          <div className="relative">
            <pre className="overflow-x-auto rounded-lg border bg-muted/40 px-3 py-2.5 pr-9 font-mono text-xs text-[var(--status-success)]">{secret}</pre>
            <CopyButton value={secret} className="absolute top-2 right-2" />
          </div>
          <DialogFooter>
            <Button onClick={() => { setSecret(null); onClose(); }}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    );
  }
  return (
    <Dialog open={open} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New invitation</DialogTitle>
        </DialogHeader>
        <form className="flex flex-col gap-3" onSubmit={(event) => { event.preventDefault(); create.mutate(); }}>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Team</Label>
            <Select value={teamId} onValueChange={setTeamId}>
              <SelectTrigger><SelectValue placeholder="default" /></SelectTrigger>
              <SelectContent>
                {teams.map((team) => (
                  <SelectItem key={team.id} value={team.id ?? ""}>{team.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Role</Label>
            <Select value={roleId} onValueChange={setRoleId}>
              <SelectTrigger><SelectValue placeholder="— pick a role —" /></SelectTrigger>
              <SelectContent>
                {roles.map((role) => (
                  <SelectItem key={role.id} value={role.id ?? ""}>{role.name}{role.builtin ? " (builtin)" : ""}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">TTL</Label>
            <Input value={ttl} onChange={(event) => setTtl(event.target.value)} placeholder="24h" />
            <p className="text-[11px] text-muted-foreground">duration form (default 24h, cap 168h)</p>
          </div>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button disabled={create.isPending} onClick={() => create.mutate()}>
            {create.isPending ? "Minting…" : "Mint invitation"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// AlertDialogLike 是本页删除确认的薄封装（标题/描述/确认词参数化——
// shadcn AlertDialog 原件，破坏性操作契约不变）。
function AlertDialogLike({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel: string;
  onConfirm: () => Promise<unknown>;
}) {
  const [pending, setPending] = useState(false);
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction asChild>
            <Button
              variant="destructive"
              disabled={pending}
              onClick={() => {
                setPending(true);
                void onConfirm().finally(() => {
                  setPending(false);
                  onOpenChange(false);
                });
              }}
            >
              {pending ? "Working…" : confirmLabel}
            </Button>
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
