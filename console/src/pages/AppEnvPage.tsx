// env 管理：列表（值脱敏——ListEnv 恒无值）+ set/remove + 逐键取明文
// （GET env 是 admin 面动作，显式展开才取）。
// **pending 分组独立可见**（票面验收项）：Set/Remove 均置 pending——
// 「待下次部署生效」分组与 effective 分开呈现。行删除走确认框
// （2026-09-25 审查 P2-4：全站破坏性动作两步确认纪律）。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, EyeOff, KeyRound, Clock, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useParams } from "react-router-dom";

import { getEnv, listEnv, removeEnv, setEnv } from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import type { EnvVarView } from "@/api/types";
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
import { useIsPlatformAdmin, useTeamCapabilities } from "@/lib/context";

function EnvRow({ app, row }: { app: string; row: EnvVarView }) {
  const queryClient = useQueryClient();
  const [revealed, setRevealed] = useState("");
  const [revealError, setRevealError] = useState<string>("");
  // 行删除确认（2026-09-25 审查 P2-4，全站破坏性动作两步确认纪律——
  // AppSecretsPage 行删除同款形态）：一键直删改为确认后删。
  const [confirmOpen, setConfirmOpen] = useState(false);
  // 角色门（前端体验门）：env 明文读 = admin+、env 写（移除）= developer+
  //（§3.2 矩阵）；服务端硬门不变，403 信封照实展示。
  const { canDeploy, canAdminResources } = useTeamCapabilities();

  const revealMutation = useMutation({
    mutationFn: () => getEnv(app, row.key ?? ""),
    onSuccess: (r) => setRevealed(r.value ?? ""),
    onError: (err) =>
      setRevealError(errorEnvelopeFrom(err).message ?? "failed to fetch value"),
  });
  const removeMutation = useMutation({
    mutationFn: () => removeEnv(app, row.key ?? ""),
    onSuccess: () => {
      setConfirmOpen(false);
      void queryClient.invalidateQueries({ queryKey: ["env", app] });
    },
  });

  return (
    <TableRow data-testid="env-row" data-key={row.key} data-status={row.status}>
      <TableCell className="font-mono text-xs">{row.key}</TableCell>
      <TableCell className="font-mono text-xs">
        {revealed !== "" ? (
          <span className="flex items-center gap-2">
            <span>{revealed}</span>
            <Button
              variant="ghost"
              size="icon"
              className="h-6 w-6"
              aria-label={`Hide value of ${row.key}`}
              onClick={() => setRevealed("")}
            >
              <EyeOff aria-hidden className="h-3.5 w-3.5" />
            </Button>
          </span>
        ) : (
          <span className="flex items-center gap-2">
            <span className="text-muted-foreground">••••••••</span>
            {canAdminResources ? (
              <Button
                variant="ghost"
                size="icon"
                className="h-6 w-6"
                aria-label={`Reveal value of ${row.key}`}
                onClick={() => {
                  setRevealError("");
                  revealMutation.mutate();
                }}
              >
                <Eye aria-hidden className="h-3.5 w-3.5" />
              </Button>
            ) : null}
          </span>
        )}
        {revealError ? (
          <span className="ml-2 text-xs text-red-600 dark:text-red-400">{revealError}</span>
        ) : null}
      </TableCell>
      <TableCell className="text-xs">
        {row.source === "system" ? (
          // source=system = 平台物化行（E4 managed-databases：FLEETLY_DB_* 连
          // 接串族）——值由库实例轮换自动跟进，用户不可写（保留前缀守卫），
          // 与用户 env 的视觉区分是诚实展示的一部分。
          <span
            data-testid="env-system-badge"
            className="inline-flex items-center rounded-md border px-1.5 py-0.5 text-xs font-medium text-muted-foreground"
          >
            system
          </span>
        ) : (
          row.source
        )}
      </TableCell>
      <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
        updated {timeAgo(row.updated_at)}
      </TableCell>
      <TableCell>
        {canDeploy ? (
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 text-red-600 dark:text-red-400"
            aria-label={`Remove ${row.key}`}
            data-testid="env-remove-button"
            onClick={() => setConfirmOpen(true)}
            disabled={removeMutation.isPending}
          >
            <Trash2 aria-hidden className="h-3.5 w-3.5" />
          </Button>
        ) : null}
        {confirmOpen ? (
          <Dialog open onOpenChange={(v) => (v ? undefined : setConfirmOpen(false))}>
            <DialogContent data-testid="env-remove-dialog">
              <DialogHeader>
                <DialogTitle>Remove {row.key}</DialogTitle>
                <DialogDescription>
                  Remove <span className="font-mono">{row.key}</span> from this
                  application? The removal takes effect on the next deployment.
                  Setting the same key again before then restores it.
                </DialogDescription>
              </DialogHeader>
              <DialogFooter>
                <Button
                  variant="outline"
                  size="sm"
                  data-testid="env-remove-cancel"
                  onClick={() => setConfirmOpen(false)}
                >
                  Cancel
                </Button>
                <Button
                  variant="destructive"
                  size="sm"
                  data-testid="env-remove-submit"
                  disabled={removeMutation.isPending}
                  onClick={() => removeMutation.mutate()}
                >
                  {removeMutation.isPending ? "Removing…" : "Remove variable"}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        ) : null}
      </TableCell>
    </TableRow>
  );
}

function SetEnvForm({ app }: { app: string }) {
  const queryClient = useQueryClient();
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [error, setError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);

  const setMutation = useMutation({
    mutationFn: () => setEnv(app, key, value),
    onSuccess: () => {
      setKey("");
      setValue("");
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ["env", app] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (key.trim() && value) setMutation.mutate();
  }

  return (
    <form className="flex items-end gap-2" onSubmit={onSubmit}>
      <div className="w-56 space-y-1.5">
        <Label htmlFor="env-key">Key</Label>
        <Input
          id="env-key"
          className="font-mono text-xs"
          placeholder="LOG_LEVEL"
          value={key}
          onChange={(e) => setKey(e.target.value)}
        />
      </div>
      <div className="flex-1 space-y-1.5">
        <Label htmlFor="env-value">Value</Label>
        <Input
          id="env-value"
          className="font-mono text-xs"
          placeholder="value (encrypted at rest)"
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
      </div>
      <Button type="submit" size="sm" disabled={!key.trim() || !value || setMutation.isPending}>
        <Plus aria-hidden className="h-3.5 w-3.5" />
        Set
      </Button>
      {error ? (
        <EnvelopeAlert className="basis-full" code={error.code} message={error.message} suggestion={error.suggestion} />
      ) : null}
    </form>
  );
}

export function AppEnvPage() {
  const { name = "" } = useParams();
  // 角色门（前端体验门，§3.2）：env 写 = developer+。平台管理员资源面恒
  // 只读（P0-3 双门）——写表单消失时以说明行明示原因，不做静默消失。
  const { canDeploy } = useTeamCapabilities();
  const isPlatformAdmin = useIsPlatformAdmin();
  const query = useQuery({
    queryKey: ["env", name],
    queryFn: () => listEnv(name),
    refetchInterval: 5000,
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

  const rows = query.data?.env_vars ?? [];
  // pending 分组独立可见：Set/Remove 都是「随下次部署生效」的排队能作。
  const pending = rows.filter((r) => r.status === "pending");
  const effective = rows.filter((r) => r.status !== "pending");

  const renderTable = (list: EnvVarView[], emptyText: string) =>
    list.length === 0 ? (
      <p className="text-sm text-muted-foreground">{emptyText}</p>
    ) : (
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Key</TableHead>
            <TableHead>Value</TableHead>
            <TableHead>Source</TableHead>
            <TableHead>Updated</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {list.map((r) => (
            <EnvRow key={`${r.status}:${r.key}`} app={name} row={r} />
          ))}
        </TableBody>
      </Table>
    );

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between space-y-0 border-b pb-3">
        <div>
          <CardTitle className="flex items-center gap-2 text-sm font-semibold">
            <KeyRound aria-hidden className="h-4 w-4 text-muted-foreground" />
            Environment variables
          </CardTitle>
          <CardDescription className="mt-1">
            Values are envelope-encrypted at rest; changes take effect on the
            next deployment.
          </CardDescription>
        </div>
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7"
          aria-label="Refresh env"
          onClick={() => void query.refetch()}
        >
          <RefreshCw aria-hidden className="h-3.5 w-3.5" />
        </Button>
      </CardHeader>
      <CardContent className="divide-y pt-0">
        {/* env 写 = developer+（§3.2 矩阵；viewer 不见写面）。平台管理员
            资源面只读（P0-3 双门）——说明行（env 无 CLI 写路径，文案如实
            不指 CLI）。 */}
        {canDeploy ? (
          <section className="py-4">
            <SetEnvForm app={name} />
          </section>
        ) : isPlatformAdmin ? (
          <section className="py-4" data-testid="platform-readonly-note">
            <p className="text-sm text-muted-foreground">
              Platform administrators have read-only access to resources
              (separation of duties). Ask a team owner for a member role to
              change environment variables.
            </p>
          </section>
        ) : null}
        <section className="py-4">
          <h3
            className="mb-3 flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-amber-600 dark:text-amber-400"
            data-testid="env-pending-heading"
          >
            <Clock aria-hidden className="h-3.5 w-3.5" />
            Pending — takes effect on next deploy ({pending.length})
          </h3>
          {renderTable(pending, "No pending changes.")}
        </section>
        <section className="py-4">
          <h3 className="mb-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Effective ({effective.length})
          </h3>
          {renderTable(effective, "No effective variables.")}
        </section>
      </CardContent>
    </Card>
  );
}
