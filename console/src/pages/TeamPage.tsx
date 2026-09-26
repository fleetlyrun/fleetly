// 团队设置页（/teams/:teamId，v0.3 W2-S5，rbac-teams 设计 §7「设置：团队
// 成员与角色、邀请管理（链接直出复制）」+ §3.3 项目覆写成员 tab）：
//   Members   成员列表（角色徽章）+ 角色变更/移除（owner 专属——§3.2 矩阵；
//             最后一名 owner 守卫 E_TEAM_LAST_OWNER 在服务端）；
//   Invites   邀请管理（owner/admin；创建〔选角色〕/列表/吊销；明文 token
//             仅创建响应一次——链接直出复制框，无 SMTP）；
//   Projects  项目列表 + 创建/删除（owner 专属）+ 项目覆写成员（admin/
//             owner；覆写角色 ∈ admin/developer/viewer，owner 不可覆写）。
// 所有写按钮按调用者团队角色渲染（前端体验门；服务端硬门不变）。
// 页签态进 URL（2026-09-25 审查 P2-2，与 SystemPage 同款 ?tab= 模式）：
// Members/Invites/Projects 可深链；非法/缺省回落 members（成员面是页首
// 主形态）。Projects tab 的项目行可点进 /projects/:id（与 ProjectsPage
// 行为对齐——此前行本体无链接，与一级页相反）。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Loader2, Plus, Trash2, UsersRound } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";

import {
  createInvite,
  createProject,
  deleteProject,
  listProjectMembers,
  listProjects,
  listTeamInvites,
  listTeamMembers,
  removeProjectMember,
  removeTeamMember,
  revokeInvite,
  setProjectMemberRole,
  setTeamMemberRole,
} from "@/api/endpoints";
import { errorEnvelopeFrom, type ErrorEnvelope } from "@/api/errors";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { PageHeader } from "@/components/page-header";
import { PillTabs } from "@/components/pill-tabs";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { capabilitiesForRole, useProjectContext } from "@/lib/context";
import { formatTime } from "@/lib/utils";

const TEAM_ROLES = ["owner", "admin", "developer", "viewer"] as const;
// 覆写角色词表（§3.3：owner 不可覆写——三档）。
const OVERRIDE_ROLES = ["admin", "developer", "viewer"] as const;

const INVITE_PATH = "auth/invite";

/** 邀请链接拼装（明文 token 一次性——创建响应直出，服务端只存哈希）。 */
function inviteLink(token: string): string {
  const base = import.meta.env.BASE_URL.replace(/\/?$/, "/");
  return `${window.location.origin}${base}${INVITE_PATH}?token=${encodeURIComponent(token)}`;
}

function InlineError({ error }: { error: ErrorEnvelope | null }) {
  if (!error) return null;
  return (
    <EnvelopeAlert code={error.code} message={error.message} suggestion={error.suggestion} docs={error.docs} />
  );
}

export function TeamPage() {
  const { teamId = "" } = useParams();
  const { teams } = useProjectContext();
  const [searchParams, setSearchParams] = useSearchParams();
  // 我的团队角色（Me 投影 by team_id）→ 能力视图（前端体验门）。
  const myRole = teams.find((t) => t.team_id === teamId)?.role ?? null;
  const caps = useMemo(() => capabilitiesForRole(myRole), [myRole]);

  const tabs = [
    { key: "members", label: "Members" },
    // 邀请面 owner/admin（§3.1）——viewer/developer 不给 tab。
    ...(caps.canInvite ? [{ key: "invites", label: "Invites" }] : []),
    { key: "projects", label: "Projects" },
  ];
  // 页签态以 URL 为源（?tab=）：不在当前可见词表（含能力门收窄后的
  // invites）的取值回落 members——深链/越权 tab 一律安全落地。
  const rawTab = searchParams.get("tab") ?? "";
  const tab = tabs.some((t) => t.key === rawTab) ? rawTab : "members";

  return (
    <div className="space-y-4" data-testid="team-page">
      <PageHeader
        title="Team settings"
        description={
          caps.role
            ? `Your role in this team: ${caps.role}. Write surfaces below follow the role matrix.`
            : "You are viewing this team without a membership role (read-only)."
        }
      />
      {/* 缺省页签不带查询串（SystemPage 同款）——URL 保持干净。 */}
      <PillTabs
        value={tab}
        onValueChange={(key) => setSearchParams(key === "members" ? {} : { tab: key })}
        items={tabs}
        ariaLabel="Team sections"
      />
      {tab === "members" ? <MembersTab teamId={teamId} caps={caps} /> : null}
      {tab === "invites" && caps.canInvite ? <InvitesTab teamId={teamId} caps={caps} /> : null}
      {tab === "projects" ? <ProjectsTab teamId={teamId} caps={caps} /> : null}
    </div>
  );
}

// ── Members（§3.1/§3.2：读 = 团队成员；角色变更/移除 = owner）────────────

function MembersTab({
  teamId,
  caps,
}: {
  teamId: string;
  caps: ReturnType<typeof capabilitiesForRole>;
}) {
  const queryClient = useQueryClient();
  const [error, setError] = useState<ErrorEnvelope | null>(null);
  const [roleDraft, setRoleDraft] = useState<Record<string, string>>({});
  const [removeTarget, setRemoveTarget] = useState<{ userId: string; label: string } | null>(null);

  const membersQuery = useQuery({
    queryKey: ["team-members", teamId],
    queryFn: () => listTeamMembers(teamId),
  });

  const roleMutation = useMutation({
    mutationFn: (input: { userId: string; role: string }) =>
      setTeamMemberRole(teamId, input.userId, input.role),
    onSuccess: () => {
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ["team-members", teamId] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  const removeMutation = useMutation({
    mutationFn: (userId: string) => removeTeamMember(teamId, userId),
    onSuccess: () => {
      setRemoveTarget(null);
      setError(null);
      // 移出成员联动清项目覆写行（服务端）——项目成员缓存一并失效。
      void queryClient.invalidateQueries({ queryKey: ["team-members", teamId] });
      void queryClient.invalidateQueries({ queryKey: ["project-members"] });
    },
    onError: (err) => {
      setRemoveTarget(null);
      setError(errorEnvelopeFrom(err));
    },
  });

  const members = membersQuery.data?.members ?? [];

  return (
    <Card data-testid="members-card">
      <CardHeader className="border-b pb-3">
        <CardTitle className="flex items-center gap-2 text-sm font-semibold">
          <UsersRound aria-hidden className="h-4 w-4 text-muted-foreground" />
          Members
        </CardTitle>
        <CardDescription className="mt-1">
          Roles: owner (team management) ⊃ admin (sensitive resources) ⊃ developer (deploys,
          terminal) ⊃ viewer (read-only). Removing a member also clears their project role
          overrides in this team.
        </CardDescription>
      </CardHeader>
      <CardContent className="pt-4">
        <InlineError error={error} />
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Member</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Since</TableHead>
              {caps.canManageTeam ? <TableHead className="w-56">Change role</TableHead> : null}
            </TableRow>
          </TableHeader>
          <TableBody>
            {members.map((m) => (
              <TableRow key={m.user_id} data-testid="member-row" data-email={m.email}>
                <TableCell>
                  <div className="font-medium">{m.display_name || m.email}</div>
                  <div className="font-mono text-xs text-muted-foreground">{m.email}</div>
                </TableCell>
                <TableCell>
                  <Badge variant="secondary" data-testid="member-role-badge">
                    {m.role}
                  </Badge>
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">{formatTime(m.created_at)}</TableCell>
                {caps.canManageTeam ? (
                  <TableCell>
                    <div className="flex items-center gap-1.5">
                      {/* 原生 select——jsdom 可测性取舍（PatPage 同款）。 */}
                      <select
                        aria-label={`Role for ${m.email}`}
                        data-testid="member-role-select"
                        className="h-8 w-28 rounded-md border border-input bg-background px-2 text-xs"
                        value={roleDraft[m.user_id ?? ""] ?? m.role ?? ""}
                        onChange={(e) =>
                          setRoleDraft((prev) => ({ ...prev, [m.user_id ?? ""]: e.target.value }))
                        }
                      >
                        {TEAM_ROLES.map((r) => (
                          <option key={r} value={r}>
                            {r}
                          </option>
                        ))}
                      </select>
                      <Button
                        variant="outline"
                        size="sm"
                        data-testid="member-role-save"
                        disabled={
                          (roleDraft[m.user_id ?? ""] ?? m.role) === m.role ||
                          roleMutation.isPending
                        }
                        onClick={() =>
                          roleMutation.mutate({
                            userId: m.user_id ?? "",
                            role: roleDraft[m.user_id ?? ""] ?? "",
                          })
                        }
                      >
                        Save
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-muted-foreground hover:text-destructive"
                        aria-label={`Remove ${m.email}`}
                        data-testid="member-remove"
                        onClick={() =>
                          setRemoveTarget({
                            userId: m.user_id ?? "",
                            label: m.display_name || m.email || "",
                          })
                        }
                      >
                        <Trash2 aria-hidden className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  </TableCell>
                ) : null}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>

      <Dialog open={removeTarget !== null} onOpenChange={(open) => !open && setRemoveTarget(null)}>
        <DialogContent data-testid="member-remove-dialog">
          <DialogHeader>
            <DialogTitle>Remove member</DialogTitle>
            <DialogDescription>
              Remove <span className="font-medium">{removeTarget?.label}</span> from this team?
              Their project role overrides in this team are cleared and their access becomes
              read-only-public immediately.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" size="sm" data-testid="member-remove-cancel" onClick={() => setRemoveTarget(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              size="sm"
              data-testid="member-remove-submit"
              disabled={removeMutation.isPending}
              onClick={() => removeTarget && removeMutation.mutate(removeTarget.userId)}
            >
              {removeMutation.isPending ? "Removing…" : "Remove member"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

// ── Invites（§3.1：owner/admin；所邀角色 ≤ 邀请者自身由服务端把关）────────

function InvitesTab({
  teamId,
  caps,
}: {
  teamId: string;
  caps: ReturnType<typeof capabilitiesForRole>;
}) {
  const queryClient = useQueryClient();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<string>("developer");
  const [error, setError] = useState<ErrorEnvelope | null>(null);
  // 创建成功的一次性投影：链接直出复制框（关闭即弃——无找回面）。
  const [createdLink, setCreatedLink] = useState("");
  const [copied, setCopied] = useState(false);

  // 可选角色 = 不高于自身（§3.1 明文；服务端仍硬校验）。
  const selectableRoles: readonly string[] = caps.canManageTeam
    ? TEAM_ROLES
    : OVERRIDE_ROLES;

  const invitesQuery = useQuery({
    queryKey: ["team-invites", teamId],
    queryFn: () => listTeamInvites(teamId),
  });

  const createMutation = useMutation({
    mutationFn: () => createInvite(teamId, { email, role }),
    onSuccess: (res) => {
      setCreatedLink(inviteLink(res.token ?? ""));
      setCopied(false);
      setEmail("");
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ["team-invites", teamId] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  const revokeMutation = useMutation({
    mutationFn: (inviteId: string) => revokeInvite(teamId, inviteId),
    onSuccess: () => {
      setError(null);
      setRevokeTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["team-invites", teamId] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  // 吊销两步确认（2026-09-25 走查：此前单击即发，无确认框——与站点
  // 两拍纪律对齐）；按钮 aria-label 沿既有（Revoke invite for <email>）。
  const [revokeTarget, setRevokeTarget] = useState<{ id: string; email: string } | null>(null);

  function onCreate(e: FormEvent) {
    e.preventDefault();
    if (email.trim()) createMutation.mutate();
  }

  const invites = invitesQuery.data?.invites ?? [];

  return (
    <div className="space-y-4" data-testid="invites-tab">
      <Card data-testid="invite-create">
        <CardHeader className="border-b pb-3">
          <CardTitle className="text-sm font-semibold">Invite a member</CardTitle>
          <CardDescription className="mt-1">
            One-time invite link, valid for 7 days. There is no email delivery — copy the link
            and share it directly. The plaintext token is shown once (the server keeps a hash).
          </CardDescription>
        </CardHeader>
        <form className="flex flex-wrap items-end gap-2 p-4" onSubmit={onCreate}>
          <div className="space-y-1.5">
            <Label htmlFor="invite-email">Email</Label>
            <Input
              id="invite-email"
              data-testid="invite-email-input"
              className="w-64"
              type="email"
              placeholder="teammate@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="invite-role">Role</Label>
            <select
              id="invite-role"
              data-testid="invite-role-select"
              className="h-9 w-36 rounded-md border border-input bg-background px-3 text-sm"
              value={role}
              onChange={(e) => setRole(e.target.value)}
            >
              {selectableRoles.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </select>
          </div>
          <Button
            type="submit"
            size="sm"
            data-testid="invite-create-submit"
            disabled={!email.trim() || createMutation.isPending}
          >
            {createMutation.isPending ? (
              <Loader2 aria-hidden className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Plus aria-hidden className="h-3.5 w-3.5" />
            )}
            Create invite
          </Button>
          <div className="basis-full">
            <InlineError error={error} />
          </div>
        </form>
      </Card>

      {/* 链接直出复制框：明文 token 一次性展示。 */}
      <Dialog open={createdLink !== ""} onOpenChange={(open) => !open && setCreatedLink("")}>
        <DialogContent data-testid="invite-link-dialog">
          <DialogHeader>
            <DialogTitle>Invite created — copy the link now</DialogTitle>
            <DialogDescription>
              This link works once and expires in 7 days. It will not be shown again.
            </DialogDescription>
          </DialogHeader>
          <div
            className="break-all rounded-md border bg-muted/40 p-3 font-mono text-xs"
            data-testid="invite-link"
          >
            {createdLink}
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              data-testid="invite-link-copy"
              onClick={() => {
                void navigator.clipboard?.writeText(createdLink);
                setCopied(true);
              }}
            >
              {copied ? (
                <Check aria-hidden className="h-3.5 w-3.5" />
              ) : (
                <Copy aria-hidden className="h-3.5 w-3.5" />
              )}
              {copied ? "Copied" : "Copy link"}
            </Button>
            <Button size="sm" data-testid="invite-link-dismiss" onClick={() => setCreatedLink("")}>
              Done
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Card>
        <CardHeader className="border-b pb-3">
          <CardTitle className="text-sm font-semibold">Invites</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          {invites.length === 0 ? (
            <p className="p-5 text-sm text-muted-foreground">No invites yet.</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Email</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Expires</TableHead>
                  <TableHead className="w-16" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {invites.map((inv) => {
                  const status = inv.revoked_at
                    ? "revoked"
                    : inv.accepted_at
                      ? "accepted"
                      : "pending";
                  return (
                    <TableRow key={inv.id} data-testid="invite-row" data-status={status}>
                      <TableCell className="font-medium">{inv.email}</TableCell>
                      <TableCell>
                        <Badge variant="secondary">{inv.role}</Badge>
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">{status}</TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {formatTime(inv.expires_at)}
                      </TableCell>
                      <TableCell>
                        {status === "pending" ? (
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7 text-muted-foreground hover:text-destructive"
                            aria-label={`Revoke invite for ${inv.email}`}
                            data-testid="invite-revoke"
                            onClick={() =>
                              setRevokeTarget({ id: inv.id ?? "", email: inv.email ?? "" })
                            }
                          >
                            <Trash2 aria-hidden className="h-3.5 w-3.5" />
                          </Button>
                        ) : null}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* 吊销两步确认：说明后果（已复制的链接即刻失效）、未确认不发请求。 */}
      {revokeTarget ? (
        <Dialog open onOpenChange={(v) => (v ? undefined : setRevokeTarget(null))}>
          <DialogContent data-testid="invite-revoke-dialog">
            <DialogHeader>
              <DialogTitle>Revoke invite for {revokeTarget.email}?</DialogTitle>
              <DialogDescription>
                The pending invite link stops working immediately: anyone who
                already received it can no longer accept. This cannot be undone
                — create a new invite to re-invite the same address.
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button
                variant="outline"
                size="sm"
                data-testid="invite-revoke-cancel"
                onClick={() => setRevokeTarget(null)}
              >
                Cancel
              </Button>
              <Button
                variant="destructive"
                size="sm"
                data-testid="invite-revoke-submit"
                disabled={revokeMutation.isPending}
                onClick={() => revokeMutation.mutate(revokeTarget.id)}
              >
                {revokeMutation.isPending ? "Revoking…" : "Revoke invite"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      ) : null}
    </div>
  );
}

// ── Projects（§3.2：创建/删除 = owner；§3.3：覆写成员面 = admin/owner）────

function ProjectsTab({
  teamId,
  caps,
}: {
  teamId: string;
  caps: ReturnType<typeof capabilitiesForRole>;
}) {
  const queryClient = useQueryClient();
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [error, setError] = useState<ErrorEnvelope | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<{ id: string; label: string } | null>(null);
  const [membersOf, setMembersOf] = useState<{ id: string; label: string } | null>(null);

  const projectsQuery = useQuery({
    queryKey: ["projects", "team", teamId],
    queryFn: () => listProjects({ team_id: teamId }),
  });
  const projects = projectsQuery.data?.projects ?? [];

  const createMutation = useMutation({
    mutationFn: () => createProject({ team_id: teamId, slug, name, description }),
    onSuccess: () => {
      setSlug("");
      setName("");
      setDescription("");
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ["projects"] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteProject(id),
    onSuccess: () => {
      setDeleteTarget(null);
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ["projects"] });
    },
    onError: (err) => {
      setDeleteTarget(null);
      setError(errorEnvelopeFrom(err));
    },
  });

  function onCreate(e: FormEvent) {
    e.preventDefault();
    if (slug.trim() && name.trim()) createMutation.mutate();
  }

  return (
    <div className="space-y-4" data-testid="projects-tab">
      {caps.canManageTeam ? (
        <Card data-testid="project-create">
          <CardHeader className="border-b pb-3">
            <CardTitle className="text-sm font-semibold">Create a project</CardTitle>
            <CardDescription className="mt-1">
              Slug is word-only ([a-z0-9], 2–32 chars), immutable, and becomes the middle segment
              of the infrastructure naming formula.
            </CardDescription>
          </CardHeader>
          <form className="flex flex-wrap items-end gap-2 p-4" onSubmit={onCreate}>
            <div className="space-y-1.5">
              <Label htmlFor="project-slug">Slug</Label>
              <Input
                id="project-slug"
                data-testid="project-slug-input"
                className="w-44 font-mono text-xs"
                placeholder="prod"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="project-name">Name</Label>
              <Input
                id="project-name"
                data-testid="project-name-input"
                className="w-56"
                placeholder="Production"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="project-description">Description</Label>
              <Input
                id="project-description"
                data-testid="project-description-input"
                className="w-72"
                placeholder="(optional)"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </div>
            <Button
              type="submit"
              size="sm"
              data-testid="project-create-submit"
              disabled={!slug.trim() || !name.trim() || createMutation.isPending}
            >
              {createMutation.isPending ? (
                <Loader2 aria-hidden className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Plus aria-hidden className="h-3.5 w-3.5" />
              )}
              Create project
            </Button>
            <div className="basis-full">
              <InlineError error={error} />
            </div>
          </form>
        </Card>
      ) : null}

      <Card>
        <CardHeader className="border-b pb-3">
          <CardTitle className="text-sm font-semibold">Projects</CardTitle>
          <CardDescription className="mt-1">
            Role overrides live inside each project — team admin/owner can scope a member to a
            different role per project (owners are never overridden).
          </CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {projects.length === 0 ? (
            <p className="p-5 text-sm text-muted-foreground">No projects yet.</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Project</TableHead>
                  <TableHead>Description</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead className="w-48" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {projects.map((p) => (
                  <TableRow key={p.id} data-testid="project-row" data-slug={p.slug}>
                    <TableCell>
                      {/* 项目行可点进详情（2026-09-25 审查 P2-2，与
                          ProjectsPage 行为对齐）——名称即链接，id 寻址。 */}
                      <Link
                        to={`/projects/${encodeURIComponent(p.id ?? "")}`}
                        className="font-medium hover:underline"
                        data-testid="project-row-link"
                      >
                        {p.name}
                      </Link>
                      {/* 跨团队展示限定形（D-W0-9）。 */}
                      <div className="font-mono text-xs text-muted-foreground">
                        {p.team_slug}/{p.slug}
                      </div>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{p.description}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">{formatTime(p.created_at)}</TableCell>
                    <TableCell>
                      <div className="flex items-center justify-end gap-1.5">
                        {caps.canInvite ? (
                          <Button
                            variant="outline"
                            size="sm"
                            data-testid="project-members-open"
                            onClick={() =>
                              setMembersOf({ id: p.id ?? "", label: `${p.team_slug}/${p.slug}` })
                            }
                          >
                            Members
                          </Button>
                        ) : null}
                        {caps.canManageTeam ? (
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7 text-muted-foreground hover:text-destructive"
                            aria-label={`Delete project ${p.slug}`}
                            data-testid="project-delete"
                            onClick={() =>
                              setDeleteTarget({ id: p.id ?? "", label: `${p.team_slug}/${p.slug}` })
                            }
                          >
                            <Trash2 aria-hidden className="h-3.5 w-3.5" />
                          </Button>
                        ) : null}
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Dialog open={deleteTarget !== null} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <DialogContent data-testid="project-delete-dialog">
          <DialogHeader>
            <DialogTitle>Delete project</DialogTitle>
            <DialogDescription>
              Delete <span className="font-medium">{deleteTarget?.label}</span>? The project must
              be empty (move or delete its apps and databases first) — the server rejects
              non-empty projects. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" size="sm" data-testid="project-delete-cancel" onClick={() => setDeleteTarget(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              size="sm"
              data-testid="project-delete-submit"
              disabled={deleteMutation.isPending}
              onClick={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
            >
              {deleteMutation.isPending ? "Deleting…" : "Delete project"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {membersOf ? (
        <ProjectMembersDialog
          projectId={membersOf.id}
          projectLabel={membersOf.label}
          teamId={teamId}
          onClose={() => setMembersOf(null)}
        />
      ) : null}
    </div>
  );
}

// ── 项目覆写成员（§3.3：队内覆写形 B——admin/owner 管理面）────────────────

function ProjectMembersDialog({
  projectId,
  projectLabel,
  teamId,
  onClose,
}: {
  projectId: string;
  projectLabel: string;
  teamId: string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [userId, setUserId] = useState("");
  const [role, setRole] = useState<string>("developer");
  const [error, setError] = useState<ErrorEnvelope | null>(null);

  const membersQuery = useQuery({
    queryKey: ["project-members", projectId],
    queryFn: () => listProjectMembers(projectId),
  });
  const teamMembersQuery = useQuery({
    queryKey: ["team-members", teamId],
    queryFn: () => listTeamMembers(teamId),
  });

  const invalidate = () => {
    setError(null);
    void queryClient.invalidateQueries({ queryKey: ["project-members", projectId] });
  };

  const setMutation = useMutation({
    mutationFn: () => setProjectMemberRole(projectId, userId, role),
    onSuccess: () => {
      setUserId("");
      invalidate();
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  const removeMutation = useMutation({
    mutationFn: (uid: string) => removeProjectMember(projectId, uid),
    onSuccess: invalidate,
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  const overrides = membersQuery.data?.members ?? [];
  // 候选 = 团队成员（§3.3：仅限团队成员；owner 行不可覆写——排除）。
  const candidates = (teamMembersQuery.data?.members ?? []).filter(
    (m) => m.role !== "owner",
  );

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-2xl" data-testid="project-members-dialog">
        <DialogHeader>
          <DialogTitle>Project members — role overrides ({projectLabel})</DialogTitle>
          <DialogDescription>
            Overrides apply per project on top of team roles (bidirectional). Members without an
            override use their team role; owners always hold owner rights and cannot be
            overridden.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <InlineError error={error} />
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Member</TableHead>
                <TableHead>Override role</TableHead>
                <TableHead className="w-16" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {overrides.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={3} className="h-16 text-center text-sm text-muted-foreground" data-testid="override-empty">
                    No overrides — every member uses their team role in this project.
                  </TableCell>
                </TableRow>
              ) : (
                overrides.map((m) => (
                  <TableRow key={m.user_id} data-testid="override-row" data-email={m.email}>
                    <TableCell>
                      <div className="text-sm font-medium">{m.display_name || m.email}</div>
                      <div className="font-mono text-xs text-muted-foreground">{m.email}</div>
                    </TableCell>
                    <TableCell>
                      <Badge variant="secondary" data-testid="override-role-badge">
                        {m.role}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-muted-foreground hover:text-destructive"
                        aria-label={`Remove override for ${m.email}`}
                        data-testid="override-remove"
                        onClick={() => removeMutation.mutate(m.user_id ?? "")}
                      >
                        <Trash2 aria-hidden className="h-3.5 w-3.5" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>

          <form
            className="flex flex-wrap items-end gap-2 border-t pt-3"
            onSubmit={(e) => {
              e.preventDefault();
              if (userId) setMutation.mutate();
            }}
          >
            <div className="space-y-1.5">
              <Label htmlFor="override-user">Team member</Label>
              <select
                id="override-user"
                data-testid="override-user-select"
                className="h-9 w-56 rounded-md border border-input bg-background px-3 text-sm"
                value={userId}
                onChange={(e) => setUserId(e.target.value)}
              >
                <option value="">Select a member…</option>
                {candidates.map((m) => (
                  <option key={m.user_id} value={m.user_id ?? ""}>
                    {m.display_name || m.email} ({m.role})
                  </option>
                ))}
              </select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="override-role">Override role</Label>
              <select
                id="override-role"
                data-testid="override-role-select"
                className="h-9 w-36 rounded-md border border-input bg-background px-3 text-sm"
                value={role}
                onChange={(e) => setRole(e.target.value)}
              >
                {OVERRIDE_ROLES.map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </select>
            </div>
            <Button
              type="submit"
              size="sm"
              data-testid="override-set-submit"
              disabled={!userId || setMutation.isPending}
            >
              {setMutation.isPending ? "Saving…" : "Set override"}
            </Button>
          </form>
        </div>

        <DialogFooter>
          <Button variant="outline" size="sm" data-testid="override-close" onClick={onClose}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
