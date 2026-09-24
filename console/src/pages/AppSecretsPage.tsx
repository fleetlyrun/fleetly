// 平台密钥库页（E4 managed-databases §2.7，W4-S6 Console 面）：app 级
// external secrets 的读写面——列表（name/hash8/updated，无值读回 D-DB-7）+
// set（覆盖即轮换）+ remove（确认后删；引用方下次部署 preflight
// E_SECRET_NOT_FOUND 诚实失败）。模式跟随 AppEnvPage（表 + 行内动作）。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useParams } from "react-router-dom";

import { listSecrets, removeSecret, setSecret } from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import type { SecretView } from "@/api/types";
import { EnvelopeAlert } from "@/components/envelope-alert";
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
import { timeAgo } from "@/lib/utils";
import { useTeamCapabilities } from "@/lib/context";

const NAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

function SecretRow({ app, row }: { app: string; row: SecretView }) {
  const queryClient = useQueryClient();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [error, setError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);
  const { canAdminResources } = useTeamCapabilities();

  const removeMutation = useMutation({
    mutationFn: () => removeSecret(app, row.name ?? ""),
    onSuccess: () => {
      setError(null);
      setConfirmOpen(false);
      void queryClient.invalidateQueries({ queryKey: ["secrets", app] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  return (
    <TableRow data-testid="secret-row" data-name={row.name}>
      <TableCell className="font-mono text-xs">{row.name}</TableCell>
      <TableCell className="font-mono text-xs text-muted-foreground" title="sha256 prefix of the value — lets you check whether a stored secret is still the value you think it is">
        {row.hash8}
      </TableCell>
      <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
        updated {timeAgo(row.updated_at)}
      </TableCell>
      <TableCell className="text-right">
        {canAdminResources ? (
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 text-red-600 dark:text-red-400"
            aria-label={`Remove secret ${row.name}`}
            data-testid="secret-remove-button"
            onClick={() => setConfirmOpen(true)}
          >
            <Trash2 aria-hidden className="h-3.5 w-3.5" />
          </Button>
        ) : null}
      </TableCell>
      {confirmOpen ? (
        <Dialog open onOpenChange={(v) => (v ? undefined : setConfirmOpen(false))}>
          <DialogContent data-testid="secret-remove-dialog">
            <DialogHeader>
              <DialogTitle>Remove secret {row.name}</DialogTitle>
              <DialogDescription>
                Values are never readable again. Services that still reference
                this secret keep running, but their next deployment fails
                honestly with E_SECRET_NOT_FOUND until the declaration is
                removed. Setting the same name again restores it.
              </DialogDescription>
            </DialogHeader>
            {error ? (
              <EnvelopeAlert code={error.code} message={error.message} suggestion={error.suggestion} />
            ) : null}
            <DialogFooter>
              <Button
                variant="destructive"
                data-testid="secret-remove-submit"
                disabled={removeMutation.isPending}
                onClick={() => removeMutation.mutate()}
              >
                {removeMutation.isPending ? "Removing…" : "Remove secret"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      ) : null}
    </TableRow>
  );
}

function SetSecretForm({ app }: { app: string }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [error, setError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);

  const setMutation = useMutation({
    mutationFn: () => setSecret(app, name, value),
    onSuccess: () => {
      setName("");
      setValue("");
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ["secrets", app] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (NAME_PATTERN.test(name) && value) setMutation.mutate();
  }

  return (
    <form className="flex items-end gap-2" onSubmit={onSubmit}>
      <div className="w-56 space-y-1.5">
        <Label htmlFor="secret-name">Name</Label>
        <Input
          id="secret-name"
          data-testid="secret-name-input"
          className="font-mono text-xs"
          placeholder="API_TOKEN"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </div>
      <div className="flex-1 space-y-1.5">
        <Label htmlFor="secret-value">Value</Label>
        <Input
          id="secret-value"
          data-testid="secret-value-input"
          type="password"
          autoComplete="off"
          className="font-mono text-xs"
          placeholder="encrypted at rest — never read back"
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
      </div>
      <Button
        type="submit"
        size="sm"
        data-testid="secret-set-submit"
        disabled={!NAME_PATTERN.test(name) || !value || setMutation.isPending}
      >
        <Plus aria-hidden className="h-3.5 w-3.5" />
        Set
      </Button>
      {error ? (
        <EnvelopeAlert className="basis-full" code={error.code} message={error.message} suggestion={error.suggestion} />
      ) : null}
    </form>
  );
}

export function AppSecretsPage() {
  const { name = "" } = useParams();
  // 角色门（前端体验门，§3.2）：secrets 写 = admin+（viewer/developer 不见
  // 写面；服务端硬门不变）。
  const { canAdminResources } = useTeamCapabilities();
  const query = useQuery({
    queryKey: ["secrets", name],
    queryFn: () => listSecrets(name),
  });

  if (query.isError) {
    const envelope = errorEnvelopeFrom(query.error);
    return (
      <EnvelopeAlert
        code={envelope.code}
        message={envelope.message}
        suggestion={envelope.suggestion}
        docs={envelope.docs}
      />
    );
  }

  const rows = query.data?.secrets ?? [];

  return (
    <Card data-testid="secrets-page">
      <CardHeader className="flex-row items-center justify-between space-y-0 border-b pb-3">
        <div>
          <CardTitle className="flex items-center gap-2 text-sm font-semibold">
            <KeyRound aria-hidden className="h-4 w-4 text-muted-foreground" />
            Platform secrets
          </CardTitle>
          <CardDescription className="mt-1">
            Write-only store for compose external secrets — values are
            envelope-encrypted at rest and never read back (forgot one? set it
            again). Services mount declarations at /run/secrets/&lt;name&gt; on
            their next deploy; rotating a value changes its fingerprint, so the
            referencing service picks it up on redeploy.
          </CardDescription>
        </div>
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7"
          aria-label="Refresh secrets"
          onClick={() => void query.refetch()}
        >
          <RefreshCw aria-hidden className="h-3.5 w-3.5" />
        </Button>
      </CardHeader>
      <CardContent className="divide-y pt-0">
        {canAdminResources ? (
          <section className="py-4">
            <SetSecretForm app={name} />
          </section>
        ) : null}
        <section className="py-4">
          {rows.length === 0 ? (
            <p className="text-sm text-muted-foreground" data-testid="secrets-empty">
              No secrets stored. Set one above, declare it under the compose
              file's top-level <code>secrets:</code> (external: true) and
              reference it from a service.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Fingerprint</TableHead>
                  <TableHead>Updated</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((r) => (
                  <SecretRow key={r.name} app={name} row={r} />
                ))}
              </TableBody>
            </Table>
          )}
        </section>
      </CardContent>
    </Card>
  );
}
