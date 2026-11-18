import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { PlusIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { useCollection, useCommand } from "@lynxland/uikit-core";
import type { ApiError, CommandResult } from "@lynxland/uikit-core/kernel";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { ErrorState } from "@/components/domain/error-state";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, type StatusTone } from "@/components/domain/status-badge";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
import { describeApiError } from "@/kit/errors";
import { functionsResource, functionsSearch, type FunctionRow } from "@/kit/functions";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";

// Functions 面板族（ADR-0102 任务⑥；设计 v3 §11——Apps/Databases/Tasks 平级
// 最小面起步）：列表（状态/exposure 徽章/replicas/revision/updated）+ 创建
// 对话框（create 即首部署）。详情动作（deploy/scale/exposure 翻转确认/回滚/
// 删除）住 function-detail；本面板持创建与删除入口（tasks 面板同形）。

export function functionStatusTone(status: string | undefined): StatusTone {
  switch (status) {
    case "running":
      return "success";
    case "degraded":
      return "warning";
    case "stopped":
      return "danger";
    default:
      return "neutral"; // pending / 未知
  }
}

function commandErrorText(
  result: CommandResult<unknown> | null,
  failure: ApiError | null,
): string | null {
  if (result != null && result.status !== "ok") {
    return result.status === "rejected" && result.code
      ? `[${result.code}] ${result.message}`
      : result.message;
  }
  if (failure != null) return describeApiError(failure);
  return null;
}

function FormError({ result, failure }: { result: CommandResult<unknown> | null; failure: ApiError | null }) {
  const text = commandErrorText(result, failure);
  if (text == null) return null;
  return (
    <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 font-mono text-2xs break-all text-muted">
      {text}
    </div>
  );
}

const EMPTY_FORM = {
  name: "",
  image: "",
  port: "8080",
  command: "",
  env: "",
  secretRefs: "",
  cpuMillis: "",
  memoryMb: "",
  minReplicas: "",
  maxReplicas: "",
  exposure: "private",
  host: "",
};

// NewFunctionButton：创建即首部署（幂等面服务端承载；host 仅 public 消费）。
export function NewFunctionButton({ projectId }: { projectId: string }) {
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({ ...EMPTY_FORM });
  const create = useCommand(functionsResource.commands.create, {
    scope: { projectId },
    onSettled: (result) => {
      if (result?.status === "ok") setOpen(false);
    },
  });
  return (
    <>
      <Button
        size="sm"
        onClick={() => {
          setForm({ ...EMPTY_FORM });
          setOpen(true);
        }}
      >
        <PlusIcon data-icon-start-inline />
        New function…
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>New function</DialogTitle>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="fn-name">Name</Label>
              <Input
                id="fn-name"
                placeholder="resize-img"
                value={form.name}
                onChange={(event) => setForm({ ...form, name: event.target.value })}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="fn-image">Image</Label>
              <Input
                id="fn-image"
                placeholder="ghcr.io/acme/resize:1"
                value={form.image}
                onChange={(event) => setForm({ ...form, image: event.target.value })}
              />
            </div>
            <div className="grid grid-cols-3 gap-3">
              <div className="grid gap-1.5">
                <Label htmlFor="fn-port">Port</Label>
                <Input
                  id="fn-port"
                  inputMode="numeric"
                  value={form.port}
                  onChange={(event) => setForm({ ...form, port: event.target.value })}
                />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="fn-min">Min replicas</Label>
                <Input
                  id="fn-min"
                  inputMode="numeric"
                  placeholder="1"
                  value={form.minReplicas}
                  onChange={(event) => setForm({ ...form, minReplicas: event.target.value })}
                />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="fn-max">Max replicas</Label>
                <Input
                  id="fn-max"
                  inputMode="numeric"
                  placeholder="1"
                  value={form.maxReplicas}
                  onChange={(event) => setForm({ ...form, maxReplicas: event.target.value })}
                />
              </div>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="fn-command">Command override (comma-separated)</Label>
              <Input
                id="fn-command"
                placeholder="/app,--verbose"
                value={form.command}
                onChange={(event) => setForm({ ...form, command: event.target.value })}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="fn-env">Env (KEY=VALUE per line)</Label>
              <Textarea
                id="fn-env"
                rows={3}
                placeholder={"LOG_LEVEL=info\nREGION=eu"}
                value={form.env}
                onChange={(event) => setForm({ ...form, env: event.target.value })}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="fn-secrets">Secret refs (one per line, mounted at /run/secrets/&lt;name&gt;)</Label>
              <Textarea
                id="fn-secrets"
                rows={2}
                placeholder="signing-key"
                value={form.secretRefs}
                onChange={(event) => setForm({ ...form, secretRefs: event.target.value })}
              />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="grid gap-1.5">
                <Label htmlFor="fn-cpu">CPU millis</Label>
                <Input
                  id="fn-cpu"
                  inputMode="numeric"
                  placeholder="unlimited"
                  value={form.cpuMillis}
                  onChange={(event) => setForm({ ...form, cpuMillis: event.target.value })}
                />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="fn-memory">Memory MiB</Label>
                <Input
                  id="fn-memory"
                  inputMode="numeric"
                  placeholder="unlimited"
                  value={form.memoryMb}
                  onChange={(event) => setForm({ ...form, memoryMb: event.target.value })}
                />
              </div>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="grid gap-1.5">
                <Label>Exposure</Label>
                <Select value={form.exposure} onValueChange={(value) => setForm({ ...form, exposure: value })}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="private">Private (project network only)</SelectItem>
                    <SelectItem value="public">Public (managed route)</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              {form.exposure === "public" && (
                <div className="grid gap-1.5">
                  <Label htmlFor="fn-host">Public host</Label>
                  <Input
                    id="fn-host"
                    placeholder="img.example.test"
                    value={form.host}
                    onChange={(event) => setForm({ ...form, host: event.target.value })}
                  />
                </div>
              )}
            </div>
            <FormError result={create.result ?? null} failure={create.failure ?? null} />
          </div>
          <DialogFooter>
            <Button
              disabled={!form.name || !form.image || create.pending}
              onClick={() => void create.mutate({ ...form })}
            >
              Create function
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

// FunctionsPanel：列表（URL 快筛/分页经 kit schema 进 URL——tasks 同形）。
export function FunctionsPanel({ projectId, from }: { projectId: string; from: string }) {
  const functions = useCollection(functionsResource, {
    from,
    search: functionsSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });

  const columns: LegacyColumnDef<FunctionRow, any>[] = [
    {
      accessorKey: "name",
      header: "Name",
      cell: ({ row }) => (
        <Link
          className="font-medium underline-offset-4 hover:underline"
          to="/p/$projectId/functions/$fnId"
          params={{ projectId, fnId: row.original.id ?? "" }}
        >
          {row.original.name}
        </Link>
      ),
    },
    {
      accessorKey: "status",
      header: "Status",
      cell: ({ row }) => (
        <StatusBadge tone={functionStatusTone(row.original.status)} pulse={row.original.status === "pending"}>
          {row.original.status ?? "pending"}
        </StatusBadge>
      ),
    },
    {
      accessorKey: "exposure",
      header: "Exposure",
      cell: ({ row }) => (
        <StatusBadge tone={row.original.exposure === "public" ? "info" : "neutral"}>
          {row.original.exposure ?? "private"}
        </StatusBadge>
      ),
    },
    {
      accessorKey: "replicas",
      header: "Replicas",
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted">
          {row.original.replicas} ({row.original.min_replicas}..{row.original.max_replicas})
        </span>
      ),
    },
    {
      accessorKey: "current_revision_seq",
      header: "Revision",
      cell: ({ row }) => <span className="font-mono text-xs text-muted">r{row.original.current_revision_seq}</span>,
    },
    {
      accessorKey: "updated_at",
      header: "Updated",
      cell: ({ row }) => <RelativeTime value={row.original.updated_at} />,
    },
  ];

  return (
    <ListPage
      list={functions}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={functions} schema={functionsSearch} textPlaceholder="Filter functions..." />
          <div className="flex items-center gap-3 py-2 pr-3">
            <CliEquivalent command="fleetly functions create" />
            <NewFunctionButton projectId={projectId} />
          </div>
        </div>
      }
      emptyTitle="No functions"
      emptyDescription="Create one — it deploys on creation and gets a permanent in-network URL."
    >
      {functions.error != null && (
        <ErrorState error={new Error(describeApiError(functions.error))} onRetry={functions.refetch} />
      )}
      <DataTable list={functions} columns={columns} getRowId={(row) => row.id ?? ""} />
      <Pagination list={functions} />
    </ListPage>
  );
}

// DeleteFunctionButton：详情/行内删除（ConfirmDialog 壳，tasks delete 同形）。
export function DeleteFunctionButton({ projectId, fn }: { projectId: string; fn: FunctionRow }) {
  const [confirmOpen, setConfirmOpen] = useState(false);
  const del = useCommand(functionsResource.commands.delete, {
    scope: { projectId },
    onSettled: (result) => {
      if (result?.status === "ok") setConfirmOpen(false);
    },
  });
  return (
    <>
      <Button variant="destructive" size="sm" onClick={() => setConfirmOpen(true)}>
        Delete…
      </Button>
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={`Delete function ${fn.name ?? fn.id ?? ""}?`}
        description="Carriers are torn down and its revisions are cleared with the aggregate; the fn DNS name dies with the row. This cannot be undone."
        confirmLabel="Delete function"
        risk={{
          tier: "wide",
          consequences: (
            <ul className="list-disc pl-4 text-xs">
              <li>Carriers are torn down and in-flight requests are cut (callers retry by their own policy).</li>
              <li>All frozen revisions are cleared with the aggregate (retention window ends here).</li>
              <li>The fn- DNS name dies with the row; ids are never reused.</li>
            </ul>
          ),
        }}
        onConfirm={() => del.mutateAsync({ id: fn.id ?? "" })}
      />
      {commandErrorText(del.result ?? null, del.failure ?? null) && (
        <div className="mt-1 text-2xs text-[var(--status-danger)]">
          {commandErrorText(del.result ?? null, del.failure ?? null)}
        </div>
      )}
    </>
  );
}
