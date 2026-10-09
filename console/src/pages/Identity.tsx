import { useState } from "react";
import { PageShell, ErrorNote, LoadingNote, EmptyNote, Modal, Field, TextInput, Select, PrimaryButton, DangerRowButton, MutationBanner, TableWrap, TableHead, useApiMutation, formatTime } from "../components/ui";
import { useUsers, useTeams, useRoles, useInvitations } from "../lib/catalog";
import type { components as identitySchemas } from "../api/identity";

type User = identitySchemas["schemas"]["v1User"];
type Team = identitySchemas["schemas"]["v1Team"];
type Role = identitySchemas["schemas"]["v1Role"];
type Invitation = identitySchemas["schemas"]["v1Invitation"];

// 身份管理页（C2 治理批）：RBAC 的 UI 面——users / teams / roles /
// invitations 四 tab，动词面对齐 CLI（identity 上下文动词组）。明文凭证
// 沿 CLI 口径：邀请 secret 只在铸造响应出现一次。
export function IdentityPage() {
  const [tab, setTab] = useState<"users" | "teams" | "roles" | "invitations">("users");
  return (
    <PageShell title="Identity" hint="users, teams, roles and invitations — the RBAC face">
      <div className="flex gap-1">
        {(["users", "teams", "roles", "invitations"] as const).map((candidate) => (
          <button
            key={candidate}
            type="button"
            onClick={() => setTab(candidate)}
            className={
              candidate === tab
                ? "rounded-md bg-slate-800 px-3 py-1.5 text-sm font-medium text-slate-100"
                : "rounded-md px-3 py-1.5 text-sm text-slate-400 hover:bg-slate-900 hover:text-slate-200"
            }
          >
            {candidate}
          </button>
        ))}
      </div>
      {tab === "users" ? <UsersTab /> : null}
      {tab === "teams" ? <TeamsTab /> : null}
      {tab === "roles" ? <RolesTab /> : null}
      {tab === "invitations" ? <InvitationsTab /> : null}
    </PageShell>
  );
}

// ---- users ----

function UsersTab() {
  const users = useUsers();
  const teams = useTeams();
  const roles = useRoles();
  const [createOpen, setCreateOpen] = useState(false);
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-slate-200">Users</h2>
        <PrimaryButton onClick={() => setCreateOpen(true)}>New user…</PrimaryButton>
      </div>
      {users.isPending ? <LoadingNote label="loading users…" /> : null}
      {users.isError ? <ErrorNote error={users.error} /> : null}
      {users.data != null ? (
        users.data.length === 0 ? (
          <EmptyNote label="No users yet — create one, or mint a token in Settings." />
        ) : (
          <TableWrap>
            <table className="w-full text-left text-sm">
              <TableHead columns={["name", "id", "created", ""]} />
              <tbody>
                {users.data.map((user: User) => (
                  <UserRow key={user.id} user={user} />
                ))}
              </tbody>
            </table>
          </TableWrap>
        )
      ) : null}
      <CreateUserModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        teams={teams.data ?? []}
        roles={roles.data ?? []}
      />
    </div>
  );
}

function UserRow({ user }: { user: User }) {
  const remove = useApiMutation({
    path: `/v1/users/${encodeURIComponent(user.id ?? "")}`,
    method: "DELETE",
    invalidate: [["identity", "users"]],
  });
  return (
    <tr className="border-t border-slate-800">
      <td className="px-3 py-2 font-medium text-slate-200">{user.name}</td>
      <td className="px-3 py-2 font-mono text-xs text-slate-500" title={user.id}>{user.id}</td>
      <td className="px-3 py-2 text-xs text-slate-500">{formatTime(user.created_at)}</td>
      <td className="px-3 py-2 text-right">
        <DangerRowButton confirm={`Delete user ${user.name}?`} disabled={remove.isPending} onClick={() => void remove.mutate()}>
          delete
        </DangerRowButton>
        {remove.isError ? <ErrorNote error={remove.error} /> : null}
      </td>
    </tr>
  );
}

function CreateUserModal({ open, onClose, teams, roles }: { open: boolean; onClose: () => void; teams: Team[]; roles: Role[] }) {
  const [name, setName] = useState("");
  const [teamId, setTeamId] = useState("");
  const [roleId, setRoleId] = useState("");
  const create = useApiMutation<{ user?: User }>({
    path: "/v1/users",
    method: "POST",
    body: () => ({ name, team_id: teamId === "" ? undefined : teamId, role_id: roleId === "" ? undefined : roleId }),
    invalidate: [["identity", "users"]],
  });
  return (
    <Modal title="New user" open={open} onClose={onClose}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          create.mutate(undefined, { onSuccess: onClose });
        }}
      >
        <Field label="Name">
          <TextInput value={name} onChange={(event) => setName(event.target.value)} placeholder="alice" autoFocus required />
        </Field>
        <Field label="Team" hint="defaults to the default team">
          <Select value={teamId} onChange={(event) => setTeamId(event.target.value)}>
            <option value="">default</option>
            {teams.map((team) => (
              <option key={team.id} value={team.id}>{team.name}</option>
            ))}
          </Select>
        </Field>
        <Field label="Role" hint="the role granted within the team">
          <Select value={roleId} onChange={(event) => setRoleId(event.target.value)}>
            <option value="">— pick a role —</option>
            {roles.map((role) => (
              <option key={role.id} value={role.id}>{role.name}{role.builtin ? " (builtin)" : ""}</option>
            ))}
          </Select>
        </Field>
        <MutationBanner pending={create.isPending} error={create.error} success={null} />
        <PrimaryButton disabled={create.isPending || name === ""}>Create user</PrimaryButton>
      </form>
    </Modal>
  );
}

// ---- teams ----

function TeamsTab() {
  const teams = useTeams();
  const [name, setName] = useState("");
  const create = useApiMutation<{ team?: Team }>({
    path: "/v1/teams",
    method: "POST",
    body: () => ({ name }),
    invalidate: [["identity", "teams"]],
  });
  return (
    <div className="flex flex-col gap-4">
      <h2 className="text-sm font-semibold text-slate-200">Teams</h2>
      {teams.isPending ? <LoadingNote label="loading teams…" /> : null}
      {teams.isError ? <ErrorNote error={teams.error} /> : null}
      {teams.data != null ? (
        teams.data.length === 0 ? (
          <EmptyNote label="No teams — the default team always exists implicitly." />
        ) : (
          <TableWrap>
            <table className="w-full text-left text-sm">
              <TableHead columns={["name", "id", "created", ""]} />
              <tbody>
                {teams.data.map((team: Team) => (
                  <TeamRow key={team.id} team={team} />
                ))}
              </tbody>
            </table>
          </TableWrap>
        )
      ) : null}
      <form
        className="flex items-end gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          create.mutate(undefined, { onSuccess: () => setName("") });
        }}
      >
        <Field label="New team">
          <TextInput value={name} onChange={(event) => setName(event.target.value)} placeholder="platform" required />
        </Field>
        <PrimaryButton disabled={create.isPending || name === ""}>Create team</PrimaryButton>
      </form>
      <MutationBanner pending={create.isPending} error={create.error} success={null} />
    </div>
  );
}

function TeamRow({ team }: { team: Team }) {
  const remove = useApiMutation({
    path: `/v1/teams/${encodeURIComponent(team.id ?? "")}`,
    method: "DELETE",
    invalidate: [["identity", "teams"]],
  });
  return (
    <tr className="border-t border-slate-800">
      <td className="px-3 py-2 font-medium text-slate-200">{team.name}</td>
      <td className="px-3 py-2 font-mono text-xs text-slate-500" title={team.id}>{team.id}</td>
      <td className="px-3 py-2 text-xs text-slate-500">{formatTime(team.created_at)}</td>
      <td className="px-3 py-2 text-right">
        <DangerRowButton confirm={`Delete team ${team.name}?`} disabled={remove.isPending} onClick={() => void remove.mutate()}>
          delete
        </DangerRowButton>
        {remove.isError ? <ErrorNote error={remove.error} /> : null}
      </td>
    </tr>
  );
}

// ---- roles ----

function RolesTab() {
  const teams = useTeams();
  const roles = useRoles();
  const [createOpen, setCreateOpen] = useState(false);
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-slate-200">Roles</h2>
        <PrimaryButton onClick={() => setCreateOpen(true)}>New role…</PrimaryButton>
      </div>
      {roles.isPending ? <LoadingNote label="loading roles…" /> : null}
      {roles.isError ? <ErrorNote error={roles.error} /> : null}
      {roles.data != null ? (
        roles.data.length === 0 ? (
          <EmptyNote label="No roles — builtin templates are listed here once queried per team." />
        ) : (
          <TableWrap>
            <table className="w-full text-left text-sm">
              <TableHead columns={["name", "team", "kind", "scopes", ""]} />
              <tbody>
                {roles.data.map((role: Role) => (
                  <RoleRow key={role.id} role={role} />
                ))}
              </tbody>
            </table>
          </TableWrap>
        )
      ) : null}
      <CreateRoleModal open={createOpen} onClose={() => setCreateOpen(false)} teams={teams.data ?? []} />
    </div>
  );
}

function RoleRow({ role }: { role: Role }) {
  const remove = useApiMutation({
    path: `/v1/roles/${encodeURIComponent(role.id ?? "")}`,
    method: "DELETE",
    invalidate: [["settings", "roles"], ["identity", "roles"]],
  });
  return (
    <tr className="border-t border-slate-800">
      <td className="px-3 py-2 font-medium text-slate-200">{role.name}</td>
      <td className="px-3 py-2 font-mono text-xs text-slate-500">{role.team_id || "—"}</td>
      <td className="px-3 py-2 text-xs text-slate-400">{role.builtin ? "builtin" : "custom"}</td>
      <td className="max-w-md px-3 py-2 font-mono text-xs text-slate-500" title={(role.scopes ?? []).join(" ")}>
        {(role.scopes ?? []).join(" ")}
      </td>
      <td className="px-3 py-2 text-right">
        {role.builtin ? null : (
          <DangerRowButton confirm={`Delete role ${role.name}?`} disabled={remove.isPending} onClick={() => void remove.mutate()}>
            delete
          </DangerRowButton>
        )}
        {remove.isError ? <ErrorNote error={remove.error} /> : null}
      </td>
    </tr>
  );
}

function CreateRoleModal({ open, onClose, teams }: { open: boolean; onClose: () => void; teams: Team[] }) {
  const [name, setName] = useState("");
  const [teamId, setTeamId] = useState("");
  const [scopes, setScopes] = useState("");
  const create = useApiMutation<{ role?: Role }>({
    path: "/v1/roles",
    method: "POST",
    body: () => ({
      name,
      team_id: teamId === "" ? undefined : teamId,
      scopes: scopes.split(/[\s,]+/).filter((part) => part !== ""),
    }),
    invalidate: [["settings", "roles"], ["identity", "roles"]],
  });
  return (
    <Modal title="New role" open={open} onClose={onClose}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          create.mutate(undefined, { onSuccess: onClose });
        }}
      >
        <Field label="Name">
          <TextInput value={name} onChange={(event) => setName(event.target.value)} placeholder="deployer" autoFocus required />
        </Field>
        <Field label="Team" hint="custom roles belong to a team">
          <Select value={teamId} onChange={(event) => setTeamId(event.target.value)}>
            <option value="">— pick a team —</option>
            {teams.map((team) => (
              <option key={team.id} value={team.id}>{team.name}</option>
            ))}
          </Select>
        </Field>
        <Field label="Scopes" hint="space-separated resource:action pairs (write implies read at enforcement)">
          <TextInput value={scopes} onChange={(event) => setScopes(event.target.value)} placeholder="apps:admin deployments:admin logs:read" spellCheck={false} required />
        </Field>
        <MutationBanner pending={create.isPending} error={create.error} success={null} />
        <PrimaryButton disabled={create.isPending || name === "" || teamId === ""}>Create role</PrimaryButton>
      </form>
    </Modal>
  );
}

// ---- invitations ----

function InvitationsTab() {
  const invitations = useInvitations();
  const teams = useTeams();
  const roles = useRoles();
  const [createOpen, setCreateOpen] = useState(false);
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-slate-200">Invitations</h2>
        <PrimaryButton onClick={() => setCreateOpen(true)}>New invitation…</PrimaryButton>
      </div>
      {invitations.isPending ? <LoadingNote label="loading invitations…" /> : null}
      {invitations.isError ? <ErrorNote error={invitations.error} /> : null}
      {invitations.data != null ? (
        invitations.data.length === 0 ? (
          <EmptyNote label="No invitations — mint one to onboard a teammate (secret is shown once)." />
        ) : (
          <TableWrap>
            <table className="w-full text-left text-sm">
              <TableHead columns={["id", "team", "role", "created by", "expires", "consumed", "created"]} />
              <tbody>
                {invitations.data.map((invitation: Invitation) => (
                  <tr key={invitation.id} className="border-t border-slate-800">
                    <td className="px-3 py-2 font-mono text-xs text-slate-400" title={invitation.id}>{invitation.id}</td>
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">{invitation.team_id}</td>
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">{invitation.role_id}</td>
                    <td className="px-3 py-2 text-xs text-slate-500">{invitation.created_by}</td>
                    <td className="px-3 py-2 text-xs text-slate-500">{formatTime(invitation.expires_at)}</td>
                    <td className="px-3 py-2 text-xs">{invitation.consumed_at ? <span className="text-slate-500">yes</span> : <span className="rounded bg-emerald-950/60 px-1.5 py-0.5 text-xs text-emerald-300">open</span>}</td>
                    <td className="px-3 py-2 text-xs text-slate-500">{formatTime(invitation.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        )
      ) : null}
      <CreateInvitationModal open={createOpen} onClose={() => setCreateOpen(false)} teams={teams.data ?? []} roles={roles.data ?? []} />
    </div>
  );
}

function CreateInvitationModal({ open, onClose, teams, roles }: { open: boolean; onClose: () => void; teams: Team[]; roles: Role[] }) {
  const [teamId, setTeamId] = useState("");
  const [roleId, setRoleId] = useState("");
  const [ttl, setTtl] = useState("24h");
  const [secret, setSecret] = useState<string | null>(null);
  const create = useApiMutation<{ invitation?: Invitation; secret?: string }>({
    path: "/v1/invitations",
    method: "POST",
    body: () => ({ team_id: teamId === "" ? undefined : teamId, role_id: roleId === "" ? undefined : roleId, ttl: ttl === "" ? undefined : ttl }),
    invalidate: [["identity", "invitations"]],
  });
  if (secret != null) {
    return (
      <Modal title="Invitation minted" open={open} onClose={() => { setSecret(null); onClose(); }}>
        <div className="flex flex-col gap-3">
          <p className="text-xs text-slate-400">
            Accept with <code className="text-slate-300">fleetly users accept --token &lt;secret&gt; --name ALICE</code>. The secret is shown once.
          </p>
          <pre className="overflow-x-auto rounded-md border border-slate-800 bg-slate-950 px-3 py-2 font-mono text-xs text-emerald-300">{secret}</pre>
          <PrimaryButton onClick={() => { setSecret(null); onClose(); }}>Done</PrimaryButton>
        </div>
      </Modal>
    );
  }
  return (
    <Modal title="New invitation" open={open} onClose={onClose}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          create.mutate(undefined, { onSuccess: (data) => setSecret(data.secret ?? "") });
        }}
      >
        <Field label="Team">
          <Select value={teamId} onChange={(event) => setTeamId(event.target.value)}>
            <option value="">default</option>
            {teams.map((team) => (
              <option key={team.id} value={team.id}>{team.name}</option>
            ))}
          </Select>
        </Field>
        <Field label="Role">
          <Select value={roleId} onChange={(event) => setRoleId(event.target.value)}>
            <option value="">— pick a role —</option>
            {roles.map((role) => (
              <option key={role.id} value={role.id}>{role.name}{role.builtin ? " (builtin)" : ""}</option>
            ))}
          </Select>
        </Field>
        <Field label="TTL" hint="duration form (e.g. 24h; default 24h, cap 168h)">
          <TextInput value={ttl} onChange={(event) => setTtl(event.target.value)} placeholder="24h" />
        </Field>
        <MutationBanner pending={create.isPending} error={create.error} success={null} />
        <PrimaryButton disabled={create.isPending}>Mint invitation</PrimaryButton>
      </form>
    </Modal>
  );
}
