// env 管理：列表（值脱敏——ListEnv 恒无值）+ set/remove + 逐键取明文
// （GET env 是 admin 面动作，显式展开才取）。
// **pending 分组独立可见**（票面验收项）：Set/Remove 均置 pending——
// 「待下次部署生效」分组与 effective 分开呈现。

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

function EnvRow({ app, row }: { app: string; row: EnvVarView }) {
  const queryClient = useQueryClient();
  const [revealed, setRevealed] = useState("");
  const [revealError, setRevealError] = useState<string>("");

  const revealMutation = useMutation({
    mutationFn: () => getEnv(app, row.key ?? ""),
    onSuccess: (r) => setRevealed(r.value ?? ""),
    onError: (err) =>
      setRevealError(errorEnvelopeFrom(err).message ?? "failed to fetch value"),
  });
  const removeMutation = useMutation({
    mutationFn: () => removeEnv(app, row.key ?? ""),
    onSuccess: () => {
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
          </span>
        )}
        {revealError ? (
          <span className="ml-2 text-xs text-red-600 dark:text-red-400">{revealError}</span>
        ) : null}
      </TableCell>
      <TableCell className="text-xs">{row.source}</TableCell>
      <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
        updated {timeAgo(row.updated_at)}
      </TableCell>
      <TableCell>
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7 text-red-600 dark:text-red-400"
          aria-label={`Remove ${row.key}`}
          onClick={() => removeMutation.mutate()}
          disabled={removeMutation.isPending}
        >
          <Trash2 aria-hidden className="h-3.5 w-3.5" />
        </Button>
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
        <section className="py-4">
          <SetEnvForm app={name} />
        </section>
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
