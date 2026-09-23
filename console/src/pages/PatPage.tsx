// PAT 自服务页（/pat，v0.3 W2-S2，rbac-teams 设计 §7「个人 PAT 页：创建
// 时显式 scope + project 绑定 + 明文一次性展示」）：列表（name/scopes/
// 绑定项目/last_used/created）+ 创建（scope 勾选 + 可选项目绑定——项目集
// 经 W2-S1 ProjectsService）+ 明文一次性展示（复制）+ 吊销。服务端语义
// （§2.3）：本页只管自己的 PAT——列表/吊销均按调用方属主收敛；scopes 声
// 明超出角色可达集时服务端 400 带指引（EnvelopeAlert 如实透出）。会话失
// 效 401 由 api 层全局处置，本页不重复处置。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, KeyRound, Loader2, Plus, Trash2 } from "lucide-react";
import { type FormEvent, useState } from "react";

import {
  createToken,
  listProjects,
  listTokens,
  revokeToken,
} from "@/api/endpoints";
import { errorEnvelopeFrom, type ErrorEnvelope } from "@/api/errors";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
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
import { formatTime, timeAgo } from "@/lib/utils";

// scope 词表（proto CreateTokenRequest.scopes 消费侧词表；admin 蕴含
// deploy/read ⊕ terminal——terminal 为独立 scope）。
const SCOPES = [
  { value: "read", hint: "Read-only access to apps, logs and status" },
  { value: "deploy", hint: "Deploy, rollback, env writes, cron triggers" },
  { value: "terminal", hint: "Web terminal sessions (not implied by read/deploy)" },
  { value: "admin", hint: "Everything incl. env plaintext and destructive actions" },
] as const;

/** 创建成功的一次性明文投影（关闭即弃——服务端只存哈希，无找回）。 */
type CreatedToken = { id: string; token: string };

export function PatPage() {
  const queryClient = useQueryClient();
  const tokensQuery = useQuery({
    queryKey: ["tokens"],
    queryFn: listTokens,
  });
  const projectsQuery = useQuery({
    queryKey: ["projects", "pat-page"],
    queryFn: listProjects,
    staleTime: 60_000,
  });

  const [note, setNote] = useState("");
  const [scopes, setScopes] = useState<string[]>(["read"]);
  const [projectId, setProjectId] = useState("none");
  const [created, setCreated] = useState<CreatedToken | null>(null);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState<ErrorEnvelope | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<{ id: string; name: string } | null>(null);

  const createMutation = useMutation({
    mutationFn: () =>
      createToken({
        scopes,
        note,
        project_id: projectId === "none" ? undefined : projectId,
      }),
    onSuccess: (res) => {
      setCreated({ id: res.id ?? "", token: res.token ?? "" });
      setCopied(false);
      setNote("");
      setScopes(["read"]);
      setProjectId("none");
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ["tokens"] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  const revokeMutation = useMutation({
    mutationFn: (id: string) => revokeToken(id),
    onSuccess: () => {
      setRevokeTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["tokens"] });
    },
    onError: (err) => {
      setRevokeTarget(null);
      setError(errorEnvelopeFrom(err));
    },
  });

  function toggleScope(scope: string) {
    setScopes((prev) =>
      prev.includes(scope) ? prev.filter((s) => s !== scope) : [...prev, scope],
    );
  }

  function onCreate(e: FormEvent) {
    e.preventDefault();
    if (note.trim() && scopes.length > 0) createMutation.mutate();
  }

  function copyToken() {
    if (!created) return;
    void navigator.clipboard?.writeText(created.token);
    setCopied(true);
  }

  const rows = tokensQuery.data?.tokens ?? [];
  const projects = projectsQuery.data?.projects ?? [];

  return (
    <div className="mx-auto w-full max-w-5xl space-y-6" data-testid="pat-page">
      <Card>
        <CardHeader className="border-b pb-3">
          <CardTitle className="flex items-center gap-2 text-sm font-semibold">
            <KeyRound aria-hidden className="h-4 w-4 text-muted-foreground" />
            Personal access tokens
          </CardTitle>
          <CardDescription className="mt-1">
            PATs authenticate the CLI (<code className="font-mono text-xs">fleetly auth login</code>)
            and API clients as <span className="font-medium">you</span>. Effective
            access is the intersection of the token scopes and your team/project
            roles. The plaintext is shown once — the server keeps only a hash.
          </CardDescription>
        </CardHeader>
        {tokensQuery.isError ? null : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Scopes</TableHead>
                <TableHead>Project</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="w-16" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={6} className="h-24 text-center text-sm text-muted-foreground" data-testid="pat-empty">
                    No tokens yet — create one below to sign in from the CLI.
                  </TableCell>
                </TableRow>
              ) : (
                rows.map((t) => (
                  <TableRow key={t.id} data-testid="pat-row" data-name={t.note}>
                    <TableCell className="font-medium">{t.note}</TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        {(t.scopes ?? []).map((s) => (
                          <Badge key={s} variant="secondary" className="font-mono text-[10px]">
                            {s}
                          </Badge>
                        ))}
                      </div>
                    </TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {t.project_id || "—"}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {t.last_used_at ? timeAgo(t.last_used_at) : "never"}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatTime(t.created_at)}
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-muted-foreground hover:text-destructive"
                        aria-label={`Revoke ${t.note}`}
                        data-testid="pat-revoke"
                        onClick={() => setRevokeTarget({ id: t.id ?? "", name: t.note ?? "" })}
                      >
                        <Trash2 aria-hidden className="h-3.5 w-3.5" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        )}
      </Card>

      <Card data-testid="pat-create">
        <CardHeader className="border-b pb-3">
          <CardTitle className="text-sm font-semibold">Create a token</CardTitle>
          <CardDescription className="mt-1">
            Pick the narrowest scope set that works. Declared scopes cannot
            exceed what your roles grant — the server rejects overshooting
            requests and the role gate still applies on every call.
          </CardDescription>
        </CardHeader>
        <form className="space-y-4 p-4" onSubmit={onCreate}>
          <div className="space-y-1.5">
            <Label htmlFor="pat-name">Name</Label>
            <Input
              id="pat-name"
              data-testid="pat-name-input"
              className="max-w-sm"
              placeholder="laptop / CI deploy"
              value={note}
              onChange={(e) => setNote(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label>Scopes</Label>
            <div className="grid gap-2 sm:grid-cols-2">
              {SCOPES.map((s) => (
                <label
                  key={s.value}
                  className="flex cursor-pointer items-start gap-2 rounded-md border p-2.5 text-sm"
                  data-testid={`pat-scope-${s.value}`}
                >
                  <input
                    type="checkbox"
                    className="mt-0.5"
                    checked={scopes.includes(s.value)}
                    onChange={() => toggleScope(s.value)}
                  />
                  <span>
                    <span className="font-mono text-xs font-semibold">{s.value}</span>
                    <span className="block text-xs text-muted-foreground">{s.hint}</span>
                  </span>
                </label>
              ))}
            </div>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="pat-project">Project binding (optional)</Label>
            {/* 原生 select（非 Radix）——单层简单下拉，jsdom 可测、键盘友好。 */}
            <select
              id="pat-project"
              data-testid="pat-project-select"
              className="h-9 w-72 rounded-md border border-input bg-background px-3 text-sm"
              value={projectId}
              onChange={(e) => setProjectId(e.target.value)}
            >
              <option value="none">Not bound (all visible projects)</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id ?? ""}>
                  {p.team_slug}/{p.slug}
                </option>
              ))}
            </select>
            <p className="text-xs text-muted-foreground">
              Binding narrows the token to one project; the role gate still applies.
            </p>
          </div>
          {error ? (
            <EnvelopeishAlert error={error} />
          ) : null}
          <Button
            type="submit"
            size="sm"
            data-testid="pat-create-submit"
            disabled={!note.trim() || scopes.length === 0 || createMutation.isPending}
          >
            {createMutation.isPending ? (
              <Loader2 aria-hidden className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Plus aria-hidden className="h-3.5 w-3.5" />
            )}
            Create token
          </Button>
        </form>
      </Card>

      {/* 明文一次性展示：acknowledge 关闭即弃——无找回面。 */}
      <Dialog open={created !== null} onOpenChange={(open) => !open && setCreated(null)}>
        <DialogContent data-testid="pat-token-dialog">
          <DialogHeader>
            <DialogTitle>Token created — copy it now</DialogTitle>
            <DialogDescription>
              This is the only time the plaintext is shown. Store it safely;
              the server keeps only a hash and it cannot be recovered.
            </DialogDescription>
          </DialogHeader>
          <div
            className="break-all rounded-md border bg-muted/40 p-3 font-mono text-xs"
            data-testid="pat-token-value"
          >
            {created?.token}
          </div>
          <DialogFooter>
            <Button variant="outline" size="sm" data-testid="pat-token-copy" onClick={copyToken}>
              {copied ? (
                <Check aria-hidden className="h-3.5 w-3.5" />
              ) : (
                <Copy aria-hidden className="h-3.5 w-3.5" />
              )}
              {copied ? "Copied" : "Copy"}
            </Button>
            <Button size="sm" data-testid="pat-token-dismiss" onClick={() => setCreated(null)}>
              Done
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 吊销两步确认（与 app secrets remove 同款破坏性纪律）。 */}
      <Dialog open={revokeTarget !== null} onOpenChange={(open) => !open && setRevokeTarget(null)}>
        <DialogContent data-testid="pat-revoke-dialog">
          <DialogHeader>
            <DialogTitle>Revoke token</DialogTitle>
            <DialogDescription>
              Revoke <span className="font-medium">{revokeTarget?.name}</span>?
              Clients using it stop authenticating immediately. This cannot be
              undone (create a fresh token instead).
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" size="sm" data-testid="pat-revoke-cancel" onClick={() => setRevokeTarget(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              size="sm"
              data-testid="pat-revoke-submit"
              disabled={revokeMutation.isPending}
              onClick={() => revokeTarget && revokeMutation.mutate(revokeTarget.id)}
            >
              {revokeMutation.isPending ? "Revoking…" : "Revoke token"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

/** 错误信封内联展示（页面局部错误——不触发全局 401 处置）。 */
function EnvelopeishAlert({ error }: { error: ErrorEnvelope }) {
  return (
    <div
      role="alert"
      data-testid="pat-error"
      className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive"
    >
      {error.code ? <span className="font-mono text-xs">{error.code}: </span> : null}
      {error.message || "Request failed"}
    </div>
  );
}
