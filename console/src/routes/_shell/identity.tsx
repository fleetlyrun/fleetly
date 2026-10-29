import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { PlusIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { toast } from "sonner";
import { useCollection, useCollectionData, useCommand } from "@lynx-go/uikit-core";
import { shortId } from "@/lib/format";
import { toastOnSettled } from "@/kit/toast";
import {
  identitySearch,
  invitationsResource,
  rolesResource,
  teamsResource,
  usersResource,
  type InvitationEntry,
  type RoleEntry,
  type TeamEntry,
  type UserEntry,
} from "@/kit/identity";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { CopyButton } from "@/components/domain/copy-button";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { ModulePage } from "@/components/domain/module-page";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge } from "@/components/domain/status-badge";
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
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

// 身份管理页（C2 治理批 → UI v2 批 5 reskin → 2026-10-26 批1 Wave2-B
// useCollection 化 + 批2 写面 useCommand 化 + 批4 伴读 kit 化）：users /
// teams / roles / invitations 四 tab（URL param），动词面对齐 CLI。明文凭证
// 沿 CLI 口径：邀请 secret 只在铸造响应出现一次（SecretReveal 形态）。列表
// 骨架换 uikit 块（kit ListPage × FilterBar × DataTable × Pagination），快筛/
// 分页首次进 URL（identitySearch 四 tab 共享实例，q/分页跨 tab 持久）；users
// 行内设密跨行展开走 DataTable 本地扩展槽 renderExtraRow（展开态归页面）。
// 写面批2 全走 kit/identity.ts 的 resource commands（四创建弹窗保留
// useState 壳只换提交面——mint→reveal 链敏感不迁 Form 块；Confirm 壳原样；
// toast 单源在 kit/toast.ts）。批4：创建弹窗下拉的伴读也换 kit 集合——固定
// 窗（size 100 整表，M1 useCollectionData）不经 URL，列表页的 q/cursor
// 残留不泄进下拉。
// useCollection 化批：validateSearch 合并形态收编 tab 键 + kit 集合参数
//（toSearchSchema 不认 tab，必须显式并入）；tab 切换走函数式 search 保
// q/分页跨 tab 持久（接受语义）。
const identityTabSearch = z.object({ tab: z.enum(["users", "teams", "roles", "invitations"]).optional() });

// 创建弹窗下拉的伴读固定窗：全量行整取（size 100），不经 URL——useCollection
// 解码的是 location 级共享键，列表页写进的 q/cursor 会误收进下拉窗形。
export const Route = createFileRoute("/_shell/identity")({
  validateSearch: (input: Record<string, unknown>) => ({
    ...identityTabSearch.parse(input),
    ...identitySearch.toSearchSchema()["~standard"].validate(input).value,
  }),
  component: IdentityPageV2,
});

function IdentityPageV2() {
  const navigate = Route.useNavigate();
  const tab = Route.useSearch({ select: (search) => search.tab ?? "users" });
  // 创建入口在各 tab 工具栏行右侧（对齐批 5 用户复裁：页头钮位退役）。
  // 切 tab 即收起未完成的创建弹窗（createOpen 提升保持；teams 的弹窗态
  // 原本就本地自管）。
  const [createOpen, setCreateOpen] = useState(false);
  return (
    <ModulePage
      title="Access"
      description="Users, teams, roles and invitations — the RBAC face"
      tabs={[
        { value: "users", label: "Users" },
        { value: "teams", label: "Teams" },
        { value: "roles", label: "Roles" },
        { value: "invitations", label: "Invitations" },
      ]}
      current={tab}
      onTabChange={(value) => {
        setCreateOpen(false);
        void navigate({ search: (prev) => ({ ...prev, tab: value }) });
      }}
    >
      {tab === "users" ? <UsersTab createOpen={createOpen} onCreateOpenChange={setCreateOpen} /> : null}
      {tab === "teams" ? <TeamsTab /> : null}
      {tab === "roles" ? <RolesTab createOpen={createOpen} onCreateOpenChange={setCreateOpen} /> : null}
      {tab === "invitations" ? <InvitationsTab createOpen={createOpen} onCreateOpenChange={setCreateOpen} /> : null}
    </ModulePage>
  );
}

// ---- 共用小件 ----
// useCommand 的 toast 单源在 kit/toast.ts（toastOnSettled）——ok 原句副作用
// 在 onOk，rejected/conflict 兜 toast.error(message)，failure 走
// describeApiError；Confirm/AlertDialog 壳与 mint→reveal 链原样（无结果盒
// 的面才走 toast 支路）。

// ---- users ----

function UsersTab({ createOpen, onCreateOpenChange }: { createOpen: boolean; onCreateOpenChange: (open: boolean) => void }) {
  // 数据面 useCollection（15s 轮询，与旧 useUsers 同拍）；写面批2 走
  // usersResource commands——ok 即同资源 kit 集合 + ["identity","users"]
  // 存量键（legacyKeys）失效，轮询是兜底不是唯一新鲜度来源。
  const list = useCollection(usersResource, {
    from: Route.id,
    search: identitySearch,
    query: { refetchInterval: 15_000 },
  });
  // 行内设密展开态归页面（单开；id 空兜底不展开）
  const [expandedUserId, setExpandedUserId] = useState<string | null>(null);

  const columns: LegacyColumnDef<UserEntry, any>[] = [
    {
      accessorKey: "name",
      header: "Name",
      meta: { sortField: "name" },
      cell: ({ row }) => <span className="text-sm font-medium">{row.original.name}</span>,
    },
    {
      accessorKey: "id",
      header: "ID",
      meta: { sortField: "id" },
      cell: ({ row }) => (
        <span className="font-mono text-2xs text-muted" title={row.original.id}>
          {shortId(row.original.id)}
          <CopyButton value={row.original.id ?? ""} />
        </span>
      ),
    },
    {
      accessorKey: "created_at",
      header: "Created",
      meta: { sortField: "created_at" },
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
        <UserActionsCell
          user={row.original}
          expanded={expandedUserId != null && row.original.id === expandedUserId}
          onTogglePassword={() =>
            setExpandedUserId((prev) => (prev === row.original.id ? null : row.original.id ?? null))
          }
        />
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
            <FilterBar list={list} schema={identitySearch} textPlaceholder="Filter users..." />
            <div className="py-2 pr-3">
              <Button size="sm" onClick={() => onCreateOpenChange(true)}>
                <PlusIcon data-icon-start-inline />
                New user…
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No users match the filter" : "No users yet"}
        emptyDescription={
          filtered ? "Adjust or clear the filter to see rows again." : "Create one, or mint a token in Settings."
        }
        appendix={<CliEquivalent command="fleetly users list" />}
      >
        <DataTable
          list={list}
          columns={columns}
          getRowId={(row) => row.id ?? ""}
          renderExtraRow={(user) =>
            expandedUserId != null && user.id === expandedUserId ? (
              <SetPasswordRow userId={user.id ?? ""} onDone={() => setExpandedUserId(null)} />
            ) : null
          }
        />
      </ListPage>
      <CreateUserDialog open={createOpen} onClose={() => onCreateOpenChange(false)} />
    </>
  );
}

// UserActionsCell：行内动作（password… 跨行展开 / delete + Confirm）——
// 写面批2 走 usersResource.commands（delete 后 kit 集合 + ["identity","users"]
// 存量键经 legacyKeys 同批失效）。
function UserActionsCell({ user, expanded, onTogglePassword }: { user: UserEntry; expanded: boolean; onTogglePassword: () => void }) {
  const [deleting, setDeleting] = useState(false);
  const remove = useCommand(usersResource.commands.remove, {
    onSettled: toastOnSettled(() => toast("User deleted")),
  });
  return (
    <div className="text-right">
      <div className="flex items-center justify-end gap-1.5">
        <Button variant="outline" size="sm" onClick={onTogglePassword}>
          {expanded ? "hide password" : "password…"}
        </Button>
        <Button variant="outline" size="sm" className="text-[var(--status-danger)]" onClick={() => setDeleting(true)}>
          delete
        </Button>
      </div>
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete user ${user.name ?? ""}?`}
        description="Tokens minted by this user lose their membership scoping. This cannot be undone."
        confirmLabel="Delete user"
        onConfirm={() => remove.mutateAsync({ id: user.id ?? "" })}
      />
    </div>
  );
}

// SetPasswordRow 是行内设密表单（admin 重置面；C6 第一期——自助改密随
// SSO 批裁决）。POST /v1/users/{id}/password，users:write scope 执法。
// renderExtraRow 槽内渲染（块侧 td p-0，故本件是 div 形态而非 TableRow）。
function SetPasswordRow({ userId, onDone }: { userId: string; onDone: () => void }) {
  const [password, setPassword] = useState("");
  const set = useCommand(usersResource.commands.setPassword, {
    onSettled: toastOnSettled(() => {
      toast("Password set");
      setPassword("");
      onDone();
    }),
  });
  return (
    <div className="bg-panel-2/30 px-3 py-2">
      <form
        className="flex flex-wrap items-end gap-2 py-1"
        onSubmit={(event) => {
          event.preventDefault();
          set.mutate({ id: userId, password });
        }}
      >
        <div className="flex flex-col gap-1">
          <Label className="text-xs">New password</Label>
          <Input type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="••••••••" autoFocus className="h-8 w-48" />
        </div>
        <Button type="submit" size="sm" disabled={set.pending || password === ""} className="mb-0.5">
          {set.pending ? "Setting…" : "Set password"}
        </Button>
        <p className="mb-1.5 text-2xs text-muted">admin reset — the user signs in with this on the password tab</p>
      </form>
    </div>
  );
}

function CreateUserDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  // 下拉伴读（批4 kit 集合化）：teams 15s / roles 60s 对齐旧 catalog
  // useTeams（RESOURCE_MS）/useRoles（CATALOG_MS）拍频。
  const teams = useCollectionData(teamsResource, { window: { size: 100 }, refetchInterval: 15_000 });
  const roles = useCollectionData(rolesResource, { window: { size: 100 }, refetchInterval: 60_000 });
  const teamRows = teams.rows;
  const roleRows = roles.rows;
  const [name, setName] = useState("");
  const [teamId, setTeamId] = useState("");
  const [roleId, setRoleId] = useState("");
  const [password, setPassword] = useState("");
  // 创建弹窗保留 useState 壳只换提交面（批2 配方）：空串选值由 run 归一。
  const create = useCommand(usersResource.commands.create, {
    onSettled: toastOnSettled(() => {
      toast("User created");
      onClose();
      setName("");
      setPassword("");
    }),
  });
  const submit = () => create.mutate({ name, teamId, roleId, password });
  return (
    <Dialog open={open} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New user</DialogTitle>
        </DialogHeader>
        <form className="flex flex-col gap-3" onSubmit={(event) => { event.preventDefault(); submit(); }}>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Name</Label>
            <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="alice" autoFocus required />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Team</Label>
            <Select value={teamId} onValueChange={setTeamId}>
              <SelectTrigger><SelectValue placeholder="default" /></SelectTrigger>
              <SelectContent>
                {teamRows.map((team) => (
                  <SelectItem key={team.id} value={team.id ?? ""}>{team.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-2xs text-muted">defaults to the default team</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Role</Label>
            <Select value={roleId} onValueChange={setRoleId}>
              <SelectTrigger><SelectValue placeholder="— pick a role —" /></SelectTrigger>
              <SelectContent>
                {roleRows.map((role) => (
                  <SelectItem key={role.id} value={role.id ?? ""}>{role.name}{role.builtin ? " (builtin)" : ""}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-2xs text-muted">the role granted within the team</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Initial password (optional)</Label>
            <Input type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="••••••••" />
            <p className="text-2xs text-muted">empty means no password login (set one later from the users row)</p>
          </div>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button disabled={create.pending || name === ""} onClick={() => submit()}>
            {create.pending ? "Creating…" : "Create user"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- teams ----

function TeamsTab() {
  // 数据面 useCollection（15s 轮询，与旧 useTeams 同拍）。
  const list = useCollection(teamsResource, {
    from: Route.id,
    search: identitySearch,
    query: { refetchInterval: 15_000 },
  });
  const [name, setName] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [deleting, setDeleting] = useState<TeamEntry | null>(null);
  // 创建弹窗 useState 壳保留（批2 配方）；旧 delete 的静默吞错（Confirm 壳
  // catch 掉）由 toast 单源补齐诚实面。
  const create = useCommand(teamsResource.commands.create, {
    onSettled: toastOnSettled(() => {
      toast("Team created");
      setName("");
      setCreateOpen(false);
    }),
  });
  const removeTeam = useCommand(teamsResource.commands.remove, {
    onSettled: toastOnSettled(() => toast("Team deleted")),
  });

  const columns: LegacyColumnDef<TeamEntry, any>[] = [
    { accessorKey: "name", header: "Name", meta: { sortField: "name" }, cell: ({ row }) => <span className="text-sm font-medium">{row.original.name}</span> },
    {
      accessorKey: "id",
      header: "ID",
      meta: { sortField: "id" },
      cell: ({ row }) => (
        <span className="font-mono text-2xs text-muted" title={row.original.id}>
          {shortId(row.original.id)}
          <CopyButton value={row.original.id ?? ""} />
        </span>
      ),
    },
    {
      accessorKey: "created_at",
      header: "Created",
      meta: { sortField: "created_at" },
      cell: ({ row }) => (
        <span className="text-xs text-muted"><RelativeTime value={row.original.created_at} /></span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => (
        <div className="text-right">
          <Button variant="outline" size="sm" className="text-[var(--status-danger)]" onClick={() => setDeleting(row.original)}>
            delete
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
            <FilterBar list={list} schema={identitySearch} textPlaceholder="Filter teams..." />
            <div className="py-2 pr-3">
              <Button size="sm" onClick={() => { setName(""); setCreateOpen(true); }}>
                <PlusIcon data-icon-start-inline />
                New team…
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No teams match the filter" : "No teams"}
        emptyDescription={
          filtered ? "Adjust or clear the filter to see rows again." : "The default team always exists implicitly."
        }
        appendix={<CliEquivalent command="fleetly teams list" />}
      >
        <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
      </ListPage>
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>New team</DialogTitle>
          </DialogHeader>
          <form
            className="flex flex-col gap-3"
            onSubmit={(event) => {
              event.preventDefault();
              create.mutate({ name });
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
            <Button disabled={create.pending || name === ""} onClick={() => create.mutate({ name })}>
              {create.pending ? "Creating…" : "Create team"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <ConfirmDialog
        open={deleting != null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        title={`Delete team ${deleting?.name ?? ""}?`}
        description="Members lose this team's role grants. This cannot be undone."
        confirmLabel="Delete team"
        onConfirm={() => removeTeam.mutateAsync({ id: deleting?.id ?? "" })}
      />
    </>
  );
}

// ---- roles ----

function RolesTab({ createOpen, onCreateOpenChange }: { createOpen: boolean; onCreateOpenChange: (open: boolean) => void }) {
  // teams 是创建弹窗下拉的伴读（批4 换 kit 集合固定窗；15s 对齐旧
  // catalog useTeams）。
  const teams = useCollectionData(teamsResource, { window: { size: 100 }, refetchInterval: 15_000 });
  // 数据面 useCollection：轮询统一 15s 取齐其余三表（旧 catalog useRoles
  // 键在 ["settings","roles"] 是 60s 前缀异类——kit fetch 直打同端点
  // /v1/roles，拍频对齐本页四表）。
  const list = useCollection(rolesResource, {
    from: Route.id,
    search: identitySearch,
    query: { refetchInterval: 15_000 },
  });
  const removeRole = useCommand(rolesResource.commands.remove, {
    onSettled: toastOnSettled(() => toast("Role deleted")),
  });

  const columns: LegacyColumnDef<RoleEntry, any>[] = [
    { accessorKey: "name", header: "Name", meta: { sortField: "name" }, cell: ({ row }) => <span className="text-sm font-medium">{row.original.name}</span> },
    {
      accessorKey: "team_id",
      header: "Team",
      meta: { sortField: "team_id" },
      cell: ({ row }) => <span className="font-mono text-2xs text-muted" title={row.original.team_id}>{row.original.team_id?.slice(0, 10) ?? "—"}</span>,
    },
    {
      accessorKey: "builtin",
      header: "Kind",
      cell: ({ row }) => (
        <StatusBadge tone={row.original.builtin ? "info" : "neutral"}>{row.original.builtin ? "builtin" : "custom"}</StatusBadge>
      ),
    },
    {
      id: "scopes",
      header: "Scopes",
      cell: ({ row }) => (
        <span
          className="block max-w-md truncate font-mono text-2xs text-muted"
          title={(row.original.scopes ?? []).join(" ")}
        >
          {(row.original.scopes ?? []).join(" ")}
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) =>
        row.original.builtin ? null : (
          <div className="text-right">
            <Button
              variant="outline"
              size="sm"
              className="text-[var(--status-danger)]"
              onClick={() => setDeleting(row.original)}
            >
              delete
            </Button>
          </div>
        ),
    },
  ];
  const [deleting, setDeleting] = useState<RoleEntry | null>(null);

  const filtered = list.query.predicate != null;
  return (
    <>
      <ListPage
        list={list}
        toolbar={
          <div className="flex flex-wrap items-center justify-between gap-2">
            <FilterBar list={list} schema={identitySearch} textPlaceholder="Filter roles..." />
            <div className="py-2 pr-3">
              <Button size="sm" onClick={() => onCreateOpenChange(true)}>
                <PlusIcon data-icon-start-inline />
                New role…
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No roles match the filter" : "No roles"}
        emptyDescription={
          filtered ? "Adjust or clear the filter to see rows again." : "Builtin templates are listed here once queried per team."
        }
        appendix={<CliEquivalent command="fleetly roles list" />}
      >
        <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
      </ListPage>
      <ConfirmDialog
        open={deleting != null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`Delete role ${deleting?.name ?? ""}?`}
        description="Users holding this role lose its scopes. This cannot be undone."
        confirmLabel="Delete role"
        onConfirm={() => removeRole.mutateAsync({ id: deleting?.id ?? "" })}
      />
      <CreateRoleDialog open={createOpen} onClose={() => onCreateOpenChange(false)} teams={teams.rows} />
    </>
  );
}

function CreateRoleDialog({ open, onClose, teams }: { open: boolean; onClose: () => void; teams: ReadonlyArray<TeamEntry> }) {
  const [name, setName] = useState("");
  const [teamId, setTeamId] = useState("");
  const [scopes, setScopes] = useState("");
  // 创建弹窗 useState 壳保留（批2 配方）；scopes 文本拆词是表单面归一，
  // 空串 teamId 由 run 归一 undefined。
  const create = useCommand(rolesResource.commands.create, {
    onSettled: toastOnSettled(() => {
      toast("Role created");
      onClose();
      setName("");
      setScopes("");
    }),
  });
  const submit = () =>
    create.mutate({
      name,
      teamId,
      scopes: scopes.split(/[\s,]+/).filter((part) => part !== ""),
    });
  return (
    <Dialog open={open} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New role</DialogTitle>
          <DialogDescription>Scopes are space-separated resource:action pairs (write implies read at enforcement).</DialogDescription>
        </DialogHeader>
        <form className="flex flex-col gap-3" onSubmit={(event) => { event.preventDefault(); submit(); }}>
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
            <p className="text-2xs text-muted">custom roles belong to a team</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Scopes</Label>
            <Input value={scopes} onChange={(event) => setScopes(event.target.value)} placeholder="apps:admin deployments:admin logs:read" spellCheck={false} required />
          </div>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button disabled={create.pending || name === "" || teamId === ""} onClick={() => submit()}>
            {create.pending ? "Creating…" : "Create role"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- invitations ----

function InvitationsTab({ createOpen, onCreateOpenChange }: { createOpen: boolean; onCreateOpenChange: (open: boolean) => void }) {
  // 数据面 useCollection（15s 轮询，与旧 useInvitations 同拍）。
  const list = useCollection(invitationsResource, {
    from: Route.id,
    search: identitySearch,
    query: { refetchInterval: 15_000 },
  });
  // 下拉伴读（批4 换 kit 集合固定窗；F1 回流批 useCollectionData 化）：
  // teams 15s / roles 60s 对齐旧 catalog useTeams/useRoles 拍频。
  const teams = useCollectionData(teamsResource, { window: { size: 100 }, refetchInterval: 15_000 });
  const roles = useCollectionData(rolesResource, { window: { size: 100 }, refetchInterval: 60_000 });

  const columns: LegacyColumnDef<InvitationEntry, any>[] = [
    {
      accessorKey: "id",
      header: "ID",
      meta: { sortField: "id" },
      cell: ({ row }) => (
        <span className="font-mono text-2xs text-muted" title={row.original.id}>
          {shortId(row.original.id)}
          <CopyButton value={row.original.id ?? ""} />
        </span>
      ),
    },
    {
      accessorKey: "team_id",
      header: "Team",
      cell: ({ row }) => (
        <span className="font-mono text-2xs text-muted" title={row.original.team_id}>
          {row.original.team_id?.slice(0, 10)}
        </span>
      ),
    },
    {
      accessorKey: "role_id",
      header: "Role",
      cell: ({ row }) => (
        <span className="font-mono text-2xs text-muted" title={row.original.role_id}>
          {row.original.role_id?.slice(0, 10)}
        </span>
      ),
    },
    {
      accessorKey: "created_by",
      header: "Created by",
      cell: ({ row }) => <span className="text-xs text-muted">{row.original.created_by}</span>,
    },
    {
      accessorKey: "expires_at",
      header: "Expires",
      cell: ({ row }) => (
        <span className="text-xs text-muted"><RelativeTime value={row.original.expires_at} /></span>
      ),
    },
    {
      accessorKey: "consumed_at",
      header: "Consumed",
      cell: ({ row }) =>
        row.original.consumed_at ? (
          <span className="text-xs text-muted">yes</span>
        ) : (
          <StatusBadge tone="success">open</StatusBadge>
        ),
    },
    {
      accessorKey: "created_at",
      header: "Created",
      meta: { sortField: "created_at" },
      cell: ({ row }) => (
        <span className="text-xs text-muted"><RelativeTime value={row.original.created_at} /></span>
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
            <FilterBar list={list} schema={identitySearch} textPlaceholder="Filter invitations..." />
            <div className="py-2 pr-3">
              <Button size="sm" onClick={() => onCreateOpenChange(true)}>
                <PlusIcon data-icon-start-inline />
                New invitation…
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No invitations match the filter" : "No invitations"}
        emptyDescription={
          filtered ? "Adjust or clear the filter to see rows again." : "Mint one to onboard a teammate — the secret is shown once."
        }
      >
        <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
      </ListPage>
      {/* invitations 无 CLI list 动词——CLI 行诚实省略 */}
      <CreateInvitationDialog
        open={createOpen}
        onClose={() => onCreateOpenChange(false)}
        teams={teams.rows}
        roles={roles.rows}
      />
    </>
  );
}

function CreateInvitationDialog({ open, onClose, teams, roles }: { open: boolean; onClose: () => void; teams: ReadonlyArray<TeamEntry>; roles: ReadonlyArray<RoleEntry> }) {
  const [teamId, setTeamId] = useState("");
  const [roleId, setRoleId] = useState("");
  const [ttl, setTtl] = useState("24h");
  const [secret, setSecret] = useState<string | null>(null);
  // mint→reveal 揭示链（批2 配方：useState 壳保留只换提交面）：ok 进一次性
  // secret 揭示卡（无 toast——旧口径）；空串选值由 run 归一。
  const create = useCommand(invitationsResource.commands.create, {
    onSettled: toastOnSettled((payload) => setSecret(payload.secret ?? "")),
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
            <pre className="overflow-x-auto rounded-lg border bg-panel-2/40 px-3 py-2.5 pr-9 font-mono text-xs text-[var(--status-success)]">{secret}</pre>
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
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            create.mutate({ teamId, roleId, ttl });
          }}
        >
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
            <p className="text-2xs text-muted">duration form (default 24h, cap 168h)</p>
          </div>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button disabled={create.pending} onClick={() => create.mutate({ teamId, roleId, ttl })}>
            {create.pending ? "Minting…" : "Mint invitation"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
