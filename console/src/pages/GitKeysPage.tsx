// Git push keys 自服务页（/git-keys，P1-8「Git push 通道不可发现」，2026-09-25
// 审查 backlog #11；proto fleetly/server/v1/gitkeys.proto）：在册公钥台账
//（名称 note/指纹 SHA256/类型/添加时间）+ 添加（authorized_keys 单行粘贴）
// + 删除（两步确认）。服务端语义（internal/api/gitkeys.go 用户化迁移，rbac
// -teams §2.3）：登录用户自服务面——key 归属用户，push 审计 actor 随署名用
// 户；Add 恒要求用户 principal（机具令牌 403，与 PAT 页同族），平台管理员
// 全列只读 + 任意删。格式校验在服务端（authorized_keys 解析 / 重复指纹 409
// / 多行 400）——前端只做非空与去空白，错误信封如实透出。会话失效 401 由
// api 层全局处置，本页不重复处置。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GitBranch, KeyRound, Loader2, Plus, Trash2 } from "lucide-react";
import { type FormEvent, useState } from "react";
import { Link } from "react-router-dom";

import { addGitKey, listGitKeys, removeGitKey } from "@/api/endpoints";
import { errorEnvelopeFrom, type ErrorEnvelope } from "@/api/errors";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
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
import { Textarea } from "@/components/ui/textarea";
import { formatTime } from "@/lib/utils";

export function GitKeysPage() {
  const queryClient = useQueryClient();
  const keysQuery = useQuery({
    queryKey: ["git-keys"],
    queryFn: listGitKeys,
  });

  const [publicKey, setPublicKey] = useState("");
  const [note, setNote] = useState("");
  const [error, setError] = useState<ErrorEnvelope | null>(null);
  const [added, setAdded] = useState<{ fingerprint: string; key_type: string } | null>(null);
  const [removeTarget, setRemoveTarget] = useState<{ id: string; name: string } | null>(null);

  const addMutation = useMutation({
    mutationFn: () =>
      addGitKey({ public_key: publicKey.trim(), note: note.trim() || undefined }),
    onSuccess: (res) => {
      setAdded({ fingerprint: res.fingerprint ?? "", key_type: res.key_type ?? "" });
      setPublicKey("");
      setNote("");
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ["git-keys"] });
    },
    onError: (err) => {
      setAdded(null);
      setError(errorEnvelopeFrom(err));
    },
  });

  const removeMutation = useMutation({
    mutationFn: (id: string) => removeGitKey(id),
    onSuccess: () => {
      setRemoveTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["git-keys"] });
    },
    onError: (err) => {
      setRemoveTarget(null);
      setError(errorEnvelopeFrom(err));
    },
  });

  function onAdd(e: FormEvent) {
    e.preventDefault();
    if (publicKey.trim()) addMutation.mutate();
  }

  const rows = keysQuery.data?.keys ?? [];

  return (
    <div className="mx-auto w-full max-w-5xl space-y-6" data-testid="git-keys-page">
      {/* 页头说明卡（P1-8 核心：通道怎么用——如实按 CLI git.go 的 push 形态
          写；hostkey 指纹归 System 页，链过去不重复）。 */}
      <Card data-testid="git-keys-about">
        <CardHeader className="border-b pb-3">
          <CardTitle className="flex items-center gap-2 text-sm font-semibold">
            <GitBranch aria-hidden className="h-4 w-4 text-muted-foreground" />
            Git push deploys
          </CardTitle>
          <CardDescription className="mt-1 space-y-1">
            <span className="block">
              Pushing to an app&apos;s git remote is a first-class deploy
              channel: the platform fetches the pushed branch and deploys it
              with zero downtime. Register the <span className="font-medium">public</span> key of
              an SSH keypair below (private keys never reach the platform),
              then push:
            </span>
            <code className="block rounded-md bg-muted/60 px-2 py-1.5 font-mono text-xs" data-testid="git-keys-push-shape">
              git push ssh://git@&lt;host&gt;:8424/&lt;app&gt;.git &lt;branch&gt;
            </code>
            <span className="block">
              Each app shows its exact push remote under Applications →
              Deployments → Deploy triggers. Verify the server host key
              fingerprint on the{" "}
              <Link to="/system" className="font-medium underline underline-offset-2">
                System page
              </Link>{" "}
              before the first connection.
            </span>
          </CardDescription>
        </CardHeader>
      </Card>

      <Card>
        <CardHeader className="border-b pb-3">
          <CardTitle className="flex items-center gap-2 text-sm font-semibold">
            <KeyRound aria-hidden className="h-4 w-4 text-muted-foreground" />
            Registered keys{" "}
            <span className="font-normal text-muted-foreground">({rows.length})</span>
          </CardTitle>
          <CardDescription className="mt-1">
            Keys you registered from this account. Removing a key rejects new
            SSH handshakes immediately; pushes already in flight finish.
          </CardDescription>
        </CardHeader>
        {keysQuery.isError ? (
          <CardContent>
            <p className="text-sm text-muted-foreground" data-testid="git-keys-list-error">
              Failed to load git keys.
            </p>
          </CardContent>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Fingerprint</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Added</TableHead>
                <TableHead className="w-16" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.length === 0 ? (
                <TableRow>
                  <TableCell
                    colSpan={5}
                    className="h-24 text-center text-sm text-muted-foreground"
                    data-testid="git-keys-empty"
                  >
                    No git keys yet — add one below, then push to deploy.
                  </TableCell>
                </TableRow>
              ) : (
                rows.map((k) => (
                  <TableRow key={k.id} data-testid="git-key-row" data-name={k.note}>
                    <TableCell className="max-w-[220px] truncate font-medium" title={k.note}>
                      {k.note || "—"}
                    </TableCell>
                    <TableCell className="font-mono text-xs" data-testid="git-key-fingerprint">
                      <span title={k.fingerprint}>{k.fingerprint}</span>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{k.key_type}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {k.created_at ? formatTime(k.created_at) : "—"}
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-muted-foreground hover:text-destructive"
                        aria-label={`Remove ${k.note || k.fingerprint}`}
                        data-testid="git-key-remove"
                        onClick={() =>
                          setRemoveTarget({ id: k.id ?? "", name: k.note || k.fingerprint || "" })
                        }
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

      <Card data-testid="git-key-add">
        <CardHeader className="border-b pb-3">
          <CardTitle className="text-sm font-semibold">Add a key</CardTitle>
          <CardDescription className="mt-1">
            Paste a single authorized_keys line of an OpenSSH public key
            (ssh-ed25519 / ssh-rsa / ecdsa-sha2-…). Format and duplicates are
            validated server-side — a key with the same fingerprint can only be
            registered once.
          </CardDescription>
        </CardHeader>
        <form className="space-y-4 p-4" onSubmit={onAdd}>
          <div className="space-y-1.5">
            <Label htmlFor="git-key-public">Public key</Label>
            <Textarea
              id="git-key-public"
              data-testid="git-key-public-input"
              className="min-h-[72px] font-mono text-xs"
              placeholder="ssh-ed25519 AAAAC3Nza... operator@laptop"
              value={publicKey}
              onChange={(e) => setPublicKey(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="git-key-note">Note (optional)</Label>
            <Input
              id="git-key-note"
              data-testid="git-key-note-input"
              className="max-w-sm"
              placeholder="operator laptop"
              value={note}
              onChange={(e) => setNote(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              Defaults to the key comment when left empty.
            </p>
          </div>
          {error ? (
            <div
              role="alert"
              data-testid="git-key-error"
              className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive"
            >
              {error.code ? <span className="font-mono text-xs">{error.code}: </span> : null}
              {error.message || "Request failed"}
            </div>
          ) : null}
          {added ? (
            <p className="text-sm text-emerald-600 dark:text-emerald-400" data-testid="git-key-added">
              Key added ({added.key_type} · {added.fingerprint}).
            </p>
          ) : null}
          <Button
            type="submit"
            size="sm"
            data-testid="git-key-add-submit"
            disabled={!publicKey.trim() || addMutation.isPending}
          >
            {addMutation.isPending ? (
              <Loader2 aria-hidden className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Plus aria-hidden className="h-3.5 w-3.5" />
            )}
            Add key
          </Button>
        </form>
      </Card>

      {/* 删除两步确认（PAT 吊销同款破坏性纪律；删除语义 = git.go rm：即时
          生效，在推连接不受影响，新握手即拒）。 */}
      <Dialog open={removeTarget !== null} onOpenChange={(open) => !open && setRemoveTarget(null)}>
        <DialogContent data-testid="git-key-remove-dialog">
          <DialogHeader>
            <DialogTitle>Remove git key</DialogTitle>
            <DialogDescription>
              Remove <span className="font-medium">{removeTarget?.name}</span>?
              Clients authenticating with it are rejected at the next SSH
              handshake (pushes already in flight are unaffected). This cannot
              be undone — add the key again to restore access.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              data-testid="git-key-remove-cancel"
              onClick={() => setRemoveTarget(null)}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              size="sm"
              data-testid="git-key-remove-submit"
              disabled={removeMutation.isPending}
              onClick={() => removeTarget && removeMutation.mutate(removeTarget.id)}
            >
              {removeMutation.isPending ? "Removing…" : "Remove key"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
