// 平台管理员页（/admin/users，v0.3 W2-S5，rbac-teams 设计 §7「平台管理员：
// 用户管理、注册开关」）：用户列表（平台管理员/禁用徽章）+ 创建用户（临
// 时口令一次性展示）+ 禁用/启用（禁用即会话与 PAT 联动吊销）+ 重置口令
// （重置即全端下线，明文一次性）+ 授予/撤销平台管理员 + 注册窗口开关。
// 页面入口仅对 is_platform_admin 渲染；服务端 is_platform_admin 硬门兜底
// （403 信封照实展示）。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Check,
  Copy,
  KeyRound,
  Loader2,
  Plus,
  ShieldCheck,
  ShieldOff,
  Trash2,
  UserRoundCheck,
  UserRoundX,
} from "lucide-react";
import { useState, type FormEvent } from "react";

import {
  createUser,
  disableUser,
  enableUser,
  getRegistrationState,
  grantPlatformAdmin,
  listTokens,
  listUsers,
  resetUserPassword,
  revokePlatformAdmin,
  revokeToken,
  setRegistration,
} from "@/api/endpoints";
import { errorEnvelopeFrom, type ErrorEnvelope } from "@/api/errors";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { PageHeader } from "@/components/page-header";
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
import { useIsPlatformAdmin } from "@/lib/context";
import { formatTime } from "@/lib/utils";

/** 一次性口令投影（关闭即弃——服务端只存哈希，无找回）。 */
type OneTimeSecret = { title: string; password: string };

// PlatformTokensCard 是平台级令牌台账（W2-1，2026-09-26 走查）：ListTokens
// 对平台管理员返回全平台令牌（票面裁决——机具令牌的平台级可见性），但 PAT
// 自服务页不再裸显这批数据（属主收敛到 Me）。本卡是它的正确落点：属主列
// （email / machine 徽章）+ 吊销。机具令牌 user_id 空 = 无用户属主。
function PlatformTokensCard() {
  const queryClient = useQueryClient();
  const tokensQuery = useQuery({
    queryKey: ["tokens"],
    queryFn: listTokens,
  });
  const usersQuery = useQuery({
    queryKey: ["users"],
    queryFn: listUsers,
    retry: false,
  });
  const [revokeTarget, setRevokeTarget] = useState<{ id: string; label: string } | null>(null);
  const revokeMutation = useMutation({
    mutationFn: (id: string) => revokeToken(id),
    onSuccess: () => {
      setRevokeTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["tokens"] });
    },
    onError: () => setRevokeTarget(null),
  });

  const emailByUserId = new Map<string, string>();
  for (const u of usersQuery.data?.users ?? []) {
    if (u.id) emailByUserId.set(u.id, u.email ?? u.display_name ?? u.id);
  }
  const tokens = tokensQuery.data?.tokens ?? [];

  return (
    <Card data-testid="admin-platform-tokens">
      <CardHeader className="border-b pb-3">
        <CardTitle className="text-sm font-semibold">Platform tokens</CardTitle>
        <CardDescription className="mt-1">
          Every non-revoked credential on the platform: per-user personal access
          tokens and machine tokens (no owner — CI/API credentials minted by
          platform admins). Revoking a token stops its clients immediately.
        </CardDescription>
      </CardHeader>
      <CardContent className="p-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Owner</TableHead>
              <TableHead>Scopes</TableHead>
              <TableHead>Last used</TableHead>
              <TableHead className="w-16" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {tokens.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="h-20 text-center text-sm text-muted-foreground" data-testid="admin-tokens-empty">
                  No active tokens on this platform.
                </TableCell>
              </TableRow>
            ) : (
              tokens.map((t) => {
                const owner = t.user_id ? emailByUserId.get(t.user_id) : undefined;
                const label = `${t.note || t.id} (${owner ?? "machine token"})`;
                return (
                  <TableRow key={t.id} data-testid="admin-token-row" data-name={t.note}>
                    <TableCell className="font-medium">{t.note}</TableCell>
                    <TableCell>
                      {t.user_id ? (
                        <span className="text-xs" data-testid="admin-token-owner">
                          {owner ?? t.user_id}
                        </span>
                      ) : (
                        <Badge
                          variant="secondary"
                          className="bg-muted text-[10px] font-semibold uppercase tracking-wide text-muted-foreground"
                          data-testid="admin-token-machine-badge"
                        >
                          machine token
                        </Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        {(t.scopes ?? []).map((s) => (
                          <Badge key={s} variant="secondary" className="font-mono text-[10px]">
                            {s}
                          </Badge>
                        ))}
                      </div>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {t.last_used_at ? formatTime(t.last_used_at) : "never"}
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-muted-foreground hover:text-destructive"
                        aria-label={`Revoke ${t.note}`}
                        data-testid="admin-token-revoke"
                        onClick={() => setRevokeTarget({ id: t.id ?? "", label })}
                      >
                        <Trash2 aria-hidden className="h-3.5 w-3.5" />
                      </Button>
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </CardContent>

      <Dialog open={revokeTarget !== null} onOpenChange={(open) => !open && setRevokeTarget(null)}>
        <DialogContent data-testid="admin-token-revoke-dialog">
          <DialogHeader>
            <DialogTitle>Revoke token</DialogTitle>
            <DialogDescription>
              Revoke <span className="font-medium">{revokeTarget?.label}</span>?
              Clients using it stop authenticating immediately. This cannot be
              undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" size="sm" data-testid="admin-token-revoke-cancel" onClick={() => setRevokeTarget(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              size="sm"
              data-testid="admin-token-revoke-submit"
              disabled={revokeMutation.isPending}
              onClick={() => revokeTarget && revokeMutation.mutate(revokeTarget.id)}
            >
              {revokeMutation.isPending ? "Revoking…" : "Revoke token"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

function AdminUsersPage() {
  const queryClient = useQueryClient();
  const [error, setError] = useState<ErrorEnvelope | null>(null);
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [secret, setSecret] = useState<OneTimeSecret | null>(null);
  const [copied, setCopied] = useState(false);
  const [confirmTarget, setConfirmTarget] = useState<{
    kind: "disable" | "reset" | "revoke-admin" | "grant-admin";
    userId: string;
    label: string;
  } | null>(null);

  const usersQuery = useQuery({
    queryKey: ["users"],
    queryFn: listUsers,
  });
  const registrationQuery = useQuery({
    queryKey: ["auth", "registration"],
    queryFn: getRegistrationState,
  });

  const invalidateUsers = () => {
    setError(null);
    void queryClient.invalidateQueries({ queryKey: ["users"] });
  };

  const createMutation = useMutation({
    mutationFn: () =>
      createUser({
        email,
        ...(displayName.trim() ? { display_name: displayName } : {}),
      }),
    onSuccess: (res) => {
      setSecret({
        title: `Temporary password for ${res.user?.email ?? email}`,
        password: res.temporary_password ?? "",
      });
      setCopied(false);
      setEmail("");
      setDisplayName("");
      invalidateUsers();
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  const disableMutation = useMutation({
    mutationFn: (id: string) => disableUser(id),
    onSuccess: () => {
      setConfirmTarget(null);
      invalidateUsers();
    },
    onError: (err) => {
      setConfirmTarget(null);
      setError(errorEnvelopeFrom(err));
    },
  });

  const enableMutation = useMutation({
    mutationFn: (id: string) => enableUser(id),
    onSuccess: invalidateUsers,
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  const resetMutation = useMutation({
    mutationFn: (id: string) => resetUserPassword(id),
    onSuccess: (res) => {
      setConfirmTarget(null);
      setSecret({ title: "New temporary password", password: res.temporary_password ?? "" });
      setCopied(false);
      // 重置即该用户全端下线（服务端同事务吊销会话）。
      invalidateUsers();
    },
    onError: (err) => {
      setConfirmTarget(null);
      setError(errorEnvelopeFrom(err));
    },
  });

  const grantMutation = useMutation({
    mutationFn: (id: string) => grantPlatformAdmin(id),
    onSuccess: () => {
      setConfirmTarget(null);
      invalidateUsers();
    },
    onError: (err) => {
      setConfirmTarget(null);
      setError(errorEnvelopeFrom(err));
    },
  });

  const revokeAdminMutation = useMutation({
    mutationFn: (id: string) => revokePlatformAdmin(id),
    onSuccess: () => {
      setConfirmTarget(null);
      invalidateUsers();
    },
    onError: (err) => {
      setConfirmTarget(null);
      setError(errorEnvelopeFrom(err));
    },
  });

  const registrationOpen = registrationQuery.data?.open === true;
  const registrationMutation = useMutation({
    mutationFn: (open: boolean) => setRegistration(open),
    onSuccess: () => {
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ["auth", "registration"] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  function onCreate(e: FormEvent) {
    e.preventDefault();
    if (email.trim()) createMutation.mutate();
  }

  const users = usersQuery.data?.users ?? [];

  return (
    <div className="space-y-4" data-testid="admin-page">
      <PageHeader
        title="User management"
        description="Platform-wide user administration. Disabled users lose all sessions and PATs; password resets sign every device out at once."
      />

      <Card data-testid="admin-create-user">
        <CardHeader className="border-b pb-3">
          <CardTitle className="flex items-center gap-2 text-sm font-semibold">
            <Plus aria-hidden className="h-4 w-4 text-muted-foreground" />
            Create a user
          </CardTitle>
          <CardDescription className="mt-1">
            Creates the user directly (registration state does not apply). The temporary password
            is shown once.
          </CardDescription>
        </CardHeader>
        <form className="flex flex-wrap items-end gap-2 p-4" onSubmit={onCreate}>
          <div className="space-y-1.5">
            <Label htmlFor="admin-user-email">Email</Label>
            <Input
              id="admin-user-email"
              data-testid="admin-user-email-input"
              className="w-64"
              type="email"
              placeholder="teammate@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="admin-user-name">Display name</Label>
            <Input
              id="admin-user-name"
              data-testid="admin-user-name-input"
              className="w-56"
              placeholder="(optional)"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </div>
          <Button
            type="submit"
            size="sm"
            data-testid="admin-user-create-submit"
            disabled={!email.trim() || createMutation.isPending}
          >
            {createMutation.isPending ? (
              <Loader2 aria-hidden className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Plus aria-hidden className="h-3.5 w-3.5" />
            )}
            Create user
          </Button>
          <div className="basis-full">
            {error ? (
              <EnvelopeAlert code={error.code} message={error.message} suggestion={error.suggestion} docs={error.docs} />
            ) : null}
          </div>
        </form>
      </Card>

      <Card>
        <CardHeader className="border-b pb-3">
          <CardTitle className="text-sm font-semibold">Users</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>User</TableHead>
                <TableHead>Flags</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="w-72">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.map((u) => {
                const disabled = Boolean(u.disabled_at);
                return (
                  <TableRow key={u.id} data-testid="admin-user-row" data-email={u.email}>
                    <TableCell>
                      <div className="font-medium">{u.display_name || u.email}</div>
                      <div className="font-mono text-xs text-muted-foreground">{u.email}</div>
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        {u.is_platform_admin ? (
                          <Badge
                            variant="secondary"
                            className="bg-amber-500/15 text-[10px] font-semibold uppercase tracking-wide text-amber-700 dark:text-amber-400"
                            data-testid="admin-user-platform-badge"
                          >
                            platform admin
                          </Badge>
                        ) : null}
                        {disabled ? (
                          <Badge
                            variant="secondary"
                            className="bg-destructive/10 text-[10px] font-semibold uppercase tracking-wide text-destructive"
                            data-testid="admin-user-disabled-badge"
                          >
                            disabled
                          </Badge>
                        ) : null}
                      </div>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{formatTime(u.created_at)}</TableCell>
                    <TableCell>
                      <div className="flex flex-wrap items-center gap-1.5">
                        {disabled ? (
                          <Button
                            variant="outline"
                            size="sm"
                            data-testid="admin-user-enable"
                            onClick={() => enableMutation.mutate(u.id ?? "")}
                          >
                            <UserRoundCheck aria-hidden className="h-3.5 w-3.5" />
                            Enable
                          </Button>
                        ) : (
                          <Button
                            variant="outline"
                            size="sm"
                            data-testid="admin-user-disable"
                            onClick={() =>
                              setConfirmTarget({
                                kind: "disable",
                                userId: u.id ?? "",
                                label: u.display_name || u.email || "",
                              })
                            }
                          >
                            <UserRoundX aria-hidden className="h-3.5 w-3.5" />
                            Disable
                          </Button>
                        )}
                        <Button
                          variant="outline"
                          size="sm"
                          data-testid="admin-user-reset"
                          onClick={() =>
                            setConfirmTarget({
                              kind: "reset",
                              userId: u.id ?? "",
                              label: u.display_name || u.email || "",
                            })
                          }
                        >
                          <KeyRound aria-hidden className="h-3.5 w-3.5" />
                          Reset password
                        </Button>
                        {u.is_platform_admin ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            data-testid="admin-user-revoke-admin"
                            onClick={() =>
                              setConfirmTarget({
                                kind: "revoke-admin",
                                userId: u.id ?? "",
                                label: u.display_name || u.email || "",
                              })
                            }
                          >
                            <ShieldOff aria-hidden className="h-3.5 w-3.5" />
                            Revoke admin
                          </Button>
                        ) : (
                          <Button
                            variant="ghost"
                            size="sm"
                            data-testid="admin-user-grant-admin"
                            onClick={() =>
                              setConfirmTarget({
                                kind: "grant-admin",
                                userId: u.id ?? "",
                                label: u.display_name || u.email || "",
                              })
                            }
                          >
                            <ShieldCheck aria-hidden className="h-3.5 w-3.5" />
                            Grant admin
                          </Button>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <PlatformTokensCard />

      <Card data-testid="admin-registration">
        <CardHeader className="border-b pb-3">
          <CardTitle className="text-sm font-semibold">Self-service registration</CardTitle>
          <CardDescription className="mt-1">
            When closed (default once users exist), only platform administrators can add users.
            The window is always open while the platform has no users at all.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex items-center gap-3 pt-4">
          {/* 原生 checkbox 开关——jsdom 可测性取舍。 */}
          <input
            id="admin-registration-toggle"
            type="checkbox"
            data-testid="admin-registration-toggle"
            checked={registrationOpen}
            disabled={registrationMutation.isPending}
            onChange={(e) => registrationMutation.mutate(e.target.checked)}
            className="h-4 w-4"
          />
          <Label htmlFor="admin-registration-toggle" className="font-normal">
            Registration open
          </Label>
          <span className="ml-auto text-xs text-muted-foreground">
            {registrationOpen ? "Open — anyone can register" : "Closed — invites and admin-created users only"}
          </span>
        </CardContent>
      </Card>

      {/* 一次性口令展示：复制 + Done 关闭即弃。 */}
      <Dialog open={secret !== null} onOpenChange={(open) => !open && setSecret(null)}>
        <DialogContent data-testid="admin-secret-dialog">
          <DialogHeader>
            <DialogTitle>{secret?.title}</DialogTitle>
            <DialogDescription>
              This is the only time the password is shown. The user should change it after first
              sign-in (password reset help-desk duty stays with platform administrators in v0.3).
            </DialogDescription>
          </DialogHeader>
          <div
            className="break-all rounded-md border bg-muted/40 p-3 font-mono text-xs"
            data-testid="admin-secret-value"
          >
            {secret?.password}
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              data-testid="admin-secret-copy"
              onClick={() => {
                if (secret) void navigator.clipboard?.writeText(secret.password);
                setCopied(true);
              }}
            >
              {copied ? (
                <Check aria-hidden className="h-3.5 w-3.5" />
              ) : (
                <Copy aria-hidden className="h-3.5 w-3.5" />
              )}
              {copied ? "Copied" : "Copy"}
            </Button>
            <Button size="sm" data-testid="admin-secret-dismiss" onClick={() => setSecret(null)}>
              Done
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 破坏性/敏感动作两步确认（disable/reset/revoke/grant 同款纪律）。 */}
      <Dialog open={confirmTarget !== null} onOpenChange={(open) => !open && setConfirmTarget(null)}>
        <DialogContent data-testid="admin-confirm-dialog">
          <DialogHeader>
            <DialogTitle>
              {confirmTarget?.kind === "disable"
                ? "Disable user"
                : confirmTarget?.kind === "reset"
                  ? "Reset password"
                  : confirmTarget?.kind === "grant-admin"
                    ? "Grant platform admin"
                    : "Revoke platform admin"}
            </DialogTitle>
            <DialogDescription>
              {confirmTarget?.kind === "disable"
                ? `Disable ${confirmTarget?.label}? All their sessions and PATs are revoked immediately; re-enable to restore access.`
                : confirmTarget?.kind === "reset"
                  ? `Reset the password of ${confirmTarget?.label}? The old password dies and every device is signed out. You will receive a one-time temporary password.`
                  : confirmTarget?.kind === "grant-admin"
                    ? `Grant platform administrator to ${confirmTarget?.label}? Platform admins have read access across all teams and control the platform face.`
                    : `Revoke platform administrator from ${confirmTarget?.label}? Make sure at least one administrator remains.`}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" size="sm" data-testid="admin-confirm-cancel" onClick={() => setConfirmTarget(null)}>
              Cancel
            </Button>
            <Button
              variant={confirmTarget?.kind === "disable" ? "destructive" : "default"}
              size="sm"
              data-testid="admin-confirm-submit"
              disabled={
                disableMutation.isPending ||
                resetMutation.isPending ||
                grantMutation.isPending ||
                revokeAdminMutation.isPending
              }
              onClick={() => {
                if (!confirmTarget) return;
                if (confirmTarget.kind === "disable") disableMutation.mutate(confirmTarget.userId);
                else if (confirmTarget.kind === "reset") resetMutation.mutate(confirmTarget.userId);
                else if (confirmTarget.kind === "grant-admin") grantMutation.mutate(confirmTarget.userId);
                else revokeAdminMutation.mutate(confirmTarget.userId);
              }}
            >
              Confirm
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

export function AdminPage() {
  const isPlatformAdmin = useIsPlatformAdmin();
  // 非管理员进入（深链）：诚实提示——不渲染管理面（服务端对全部方法仍硬
  // 门 is_platform_admin，此处只是不展示不可用的面）。
  if (!isPlatformAdmin) {
    return (
      <div className="space-y-4" data-testid="admin-page-denied">
        <PageHeader title="User management" />
        <EnvelopeAlert
          code="E_PERMISSION_DENIED"
          message="Platform administrator access is required for user management."
          suggestion="Ask an existing platform administrator to grant you the flag (see team design §3.2)."
        />
      </div>
    );
  }
  return <AdminUsersPage />;
}
