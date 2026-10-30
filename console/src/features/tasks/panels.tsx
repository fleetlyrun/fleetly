import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { PlusIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { useCollection, useCommand } from "@lynx-go/uikit-core";
import type { ApiError, CommandResult } from "@lynx-go/uikit-core/kernel";
import { shortId } from "@/lib/format";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { ErrorState } from "@/components/domain/error-state";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { RelativeTime } from "@/components/domain/relative-time";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
import { describeApiError } from "@/kit/errors";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { TimezoneCombobox, detectedTimezone } from "./timezone-combobox";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import {
  runsResource,
  runsLookup,
  schedulesResource,
  schedulesSearch,
  stopRunCommand,
  tasksResource,
  tasksSearch,
  type ScheduleEntry,
  type TaskEntry,
} from "@/kit/tasks";

// Tasks 面板族（UI v2 批 4 → 模板化收编 ListCard → 2026-10-26 批1
// useCollection 化 → 批2 写路径命令化）：Task 双形态（one-shot/resident 池）
// + Run 观察 + 时区 cron Schedule。动词面对齐 CLI（create/scale/stop/
// delete/renew + schedules create/trigger/delete + runs stop）。项目语境改
// 为入参；from=路由 id 由路由文件注入（panels 不反查路由，避免 route ↔
// feature 循环引用）。列表骨架换 uikit 块（kit ListPage × FilterBar ×
// DataTable × Pagination），快筛/分页首次进 URL（tasksSearch 双 tab 共享
// 实例）；runs 跨行展开走 DataTable 本地扩展槽 renderExtraRow（展开态归页
// 面）。批2：写面 useCommand（kit/tasks.ts 的 defineResource commands +
// runs 裸 descriptor），弹窗保留 useState 壳（复杂表单不迁 useCommandForm），
// ConfirmDialog 壳不动；错误披露走结果两分（rejected/conflict = result，
// 网络/5xx = failure→describeApiError）。

// kvLines 文本域 → 重复旗标面（K=V 行）。
function kvLines(text: string): Array<string> {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "" && !line.startsWith("#"));
}

// 命令错误面的两分文案：结果通道（rejected/conflict）message-first（rejected
// 带信封 code 前缀，机器判别面保留）；失败通道（kernel ApiError）走
// describeApiError 单源。null 双通道都空 = 不渲染。
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

// FormError 是弹窗表单的提交错误面（danger 信封盒，结果两分单源）。
function FormError({ result, failure }: { result: CommandResult<unknown> | null; failure: ApiError | null }) {
  const text = commandErrorText(result, failure);
  if (text == null) return null;
  return (
    <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 font-mono text-2xs break-all text-muted">
      {text}
    </div>
  );
}

// RowError 是行内动作失败的披露行（v2 口径：结果两分文案单源）。
function RowError({ result, failure }: { result: CommandResult<unknown> | null; failure: ApiError | null }) {
  const text = commandErrorText(result, failure);
  if (text == null) return null;
  return <div className="mt-1 text-2xs text-[var(--status-danger)]">{text}</div>;
}

// NewTaskButton 是 Tasks 工具栏主创建钮——弹窗与命令自包含（useState 壳
// 保留，提交面走 tasksResource.commands.create）。
export function NewTaskButton({ projectId }: { projectId: string }) {
  const [createOpen, setCreateOpen] = useState(false);
  const [form, setForm] = useState({
    name: "",
    image: "",
    form: "one-shot",
    concurrency: "1",
    ttl: "3600",
    networkGroup: "",
    command: "",
    env: "",
    secretRefs: "",
    cpuMillis: "",
    memoryMb: "",
  });
  const create = useCommand(tasksResource.commands.create, {
    scope: { projectId },
    // ok 关窗（沿用旧 onSuccess 行为；无成功 toast——现句为无）
    onSettled: (result) => {
      if (result?.status === "ok") setCreateOpen(false);
    },
  });
  return (
    <>
      <Button
        size="sm"
        onClick={() => {
          setForm({ ...form, name: "", image: "" });
          setCreateOpen(true);
        }}
      >
        <PlusIcon data-icon-start-inline />
        New task…
      </Button>
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>New task</DialogTitle>
          </DialogHeader>
          <form
            className="flex flex-col gap-3"
            onSubmit={(event) => {
              event.preventDefault();
              create.mutate({
                name: form.name || undefined,
                image: form.image,
                form: form.form === "resident" ? "resident" : undefined,
                desired_concurrency: form.form === "resident" ? Number(form.concurrency) : undefined,
                ttl_seconds: form.ttl === "" ? undefined : Number(form.ttl),
                network_group: form.networkGroup || undefined,
                command: form.command === "" ? undefined : form.command.split(/\s+/).filter((part) => part !== ""),
                env: form.env === "" ? undefined : Object.fromEntries(kvLines(form.env).map((line) => [line.slice(0, line.indexOf("=")), line.slice(line.indexOf("=") + 1)])),
                secret_refs: form.secretRefs === "" ? undefined : form.secretRefs.split(/[,\s]+/).filter((part) => part !== ""),
                cpu_millis: form.cpuMillis === "" ? undefined : Number(form.cpuMillis),
                memory_mb: form.memoryMb === "" ? undefined : Number(form.memoryMb),
              });
            }}
          >
            <div className="grid grid-cols-2 gap-3">
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Image</span>
                <Input value={form.image} onChange={(event) => setForm({ ...form, image: event.target.value })} placeholder="busybox:1.37" autoFocus />
              </Label>
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Name</span>
                <Input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} />
                <span className="text-2xs font-normal text-muted">optional</span>
              </Label>
            </div>
            <div className="grid grid-cols-3 gap-3">
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Form</span>
                <Select value={form.form} onValueChange={(value) => setForm({ ...form, form: value })}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="one-shot">one-shot</SelectItem>
                    <SelectItem value="resident">resident pool</SelectItem>
                  </SelectContent>
                </Select>
              </Label>
              {form.form === "resident" ? (
                <Label className="flex flex-col gap-1.5">
                  <span className="text-xs font-semibold">Concurrency</span>
                  <Input value={form.concurrency} onChange={(event) => setForm({ ...form, concurrency: event.target.value })} inputMode="numeric" />
                </Label>
              ) : null}
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">TTL seconds</span>
                <Input value={form.ttl} onChange={(event) => setForm({ ...form, ttl: event.target.value })} inputMode="numeric" />
                <span className="text-2xs font-normal text-muted">lease lifetime</span>
              </Label>
            </div>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Network group</span>
              <Input value={form.networkGroup} onChange={(event) => setForm({ ...form, networkGroup: event.target.value })} placeholder="dispatcher" />
              <span className="text-2xs font-normal text-muted">optional dedicated network group (cross-attach via taskGroup:&lt;name&gt;)</span>
            </Label>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Command</span>
              <Input value={form.command} onChange={(event) => setForm({ ...form, command: event.target.value })} placeholder="sh -c migrate up" />
              <span className="text-2xs font-normal text-muted">argv elements separated by spaces</span>
            </Label>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Env</span>
              <Textarea rows={2} value={form.env} onChange={(event) => setForm({ ...form, env: event.target.value })} />
              <span className="text-2xs font-normal text-muted">one KEY=VALUE per line</span>
            </Label>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Secret refs</span>
              <Input value={form.secretRefs} onChange={(event) => setForm({ ...form, secretRefs: event.target.value })} />
              <span className="text-2xs font-normal text-muted">comma-separated project secret names</span>
            </Label>
            <div className="grid grid-cols-2 gap-3">
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">CPU millis</span>
                <Input value={form.cpuMillis} onChange={(event) => setForm({ ...form, cpuMillis: event.target.value })} inputMode="numeric" />
              </Label>
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Memory MB</span>
                <Input value={form.memoryMb} onChange={(event) => setForm({ ...form, memoryMb: event.target.value })} inputMode="numeric" />
              </Label>
            </div>
            {create.result?.status === "ok" ? null : <FormError result={create.result} failure={create.failure} />}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={create.pending || form.image === ""}>
                {create.pending ? "Creating…" : "Create"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}

// TasksPanel：Task 表（runs 跨行展开）+ scale/stop/renew/delete。数据面
// useCollection（15s 轮询，与旧 useTasks 同拍）；行点击无导航（Task 名
// 链接进详情，行为保持）。
export function TasksPanel({ projectId, from }: { projectId: string; from: string }) {
  const list = useCollection(tasksResource, {
    from,
    search: tasksSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  // runs 展开态归页面（单开；id 空兜底不展开）
  const [expandedTaskId, setExpandedTaskId] = useState<string | null>(null);

  const columns: LegacyColumnDef<TaskEntry, any>[] = [
    {
      accessorKey: "name",
      header: "Task",
      meta: { sortField: "name" },
      cell: ({ row }) => (
        <div>
          <div className="font-medium">
            <Link
              to="/p/$projectId/tasks/$taskId"
              params={{ projectId: row.original.project_id ?? projectId, taskId: row.original.id ?? "" }}
              className="hover:underline"
            >
              {row.original.name || shortId(row.original.id)}
            </Link>
          </div>
          <div className="font-mono text-xs text-muted" title={row.original.id}>
            {shortId(row.original.id)}
          </div>
        </div>
      ),
    },
    {
      accessorKey: "form",
      header: "Form",
      meta: { sortField: "form" },
      cell: ({ row }) => <span className="text-xs text-muted">{row.original.form}</span>,
    },
    {
      accessorKey: "state",
      header: "State",
      meta: { sortField: "state" },
      cell: ({ row }) => <span className="text-xs text-muted">{row.original.state ?? "—"}</span>,
    },
    {
      accessorKey: "image",
      header: "Image",
      meta: { sortField: "image" },
      cell: ({ row }) => <span className="font-mono text-xs text-muted">{row.original.image}</span>,
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => (
        <TaskActionsCell
          task={row.original}
          projectId={projectId}
          expanded={expandedTaskId != null && row.original.id === expandedTaskId}
          onToggleRuns={() =>
            setExpandedTaskId((prev) => (prev === row.original.id ? null : row.original.id ?? null))
          }
        />
      ),
    },
  ];

  const filtered = list.query.predicate != null;
  return (
    <ListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={list} schema={tasksSearch} textPlaceholder="Filter tasks..." />
          <div className="py-2 pr-3">
            <NewTaskButton projectId={projectId} />
          </div>
        </div>
      }
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No tasks match the filter" : "No tasks"}
      emptyDescription={
        filtered ? "Adjust or clear the filter to see rows again." : "Create a one-shot task or a resident pool."
      }
      appendix={<CliEquivalent command={`fleetly tasks list --project ${projectId}`} />}
    >
      <DataTable
        list={list}
        columns={columns}
        getRowId={(row) => row.id ?? ""}
        renderExtraRow={(task) =>
          expandedTaskId != null && task.id === expandedTaskId ? (
            <RunsPanel projectId={projectId} from={from} taskId={task.id} />
          ) : null
        }
      />
    </ListPage>
  );
}

// TaskActionsCell：行内动作（runs 展开 / resident scale / renew / stop /
// delete + 行内错误披露 + Confirm）——批2 迁 kit 命令面（scope 显式注入：
// app 未挂 ResourceScopeProvider，缺省 scope 会错失 kit 键相交失效）。
function TaskActionsCell({
  task,
  projectId,
  expanded,
  onToggleRuns,
}: {
  task: TaskEntry;
  projectId: string;
  expanded: boolean;
  onToggleRuns: () => void;
}) {
  const [scaleTo, setScaleTo] = useState("");
  const [stopping, setStopping] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const taskId = task.id ?? "";
  const scope = { projectId };
  const scale = useCommand(tasksResource.commands.scale, { scope });
  const stop = useCommand(tasksResource.commands.stop, { scope });
  const renew = useCommand(tasksResource.commands.renew, { scope });
  const del = useCommand(tasksResource.commands.delete, { scope });
  return (
    <div className="flex flex-col items-end">
      <div className="flex flex-wrap items-center justify-end gap-1">
        <Button variant="outline" size="sm" onClick={onToggleRuns}>
          {expanded ? "hide runs" : "runs"}
        </Button>
        {task.form === "resident" ? (
          <span className="flex items-center gap-1">
            <Input
              value={scaleTo}
              onChange={(event) => setScaleTo(event.target.value)}
              placeholder="n"
              className="h-7 w-14 bg-panel-2/40 px-1.5 text-xs"
            />
            <Button
              variant="outline"
              size="sm"
              disabled={scale.pending || scaleTo === ""}
              onClick={() => void scale.mutate({ id: taskId, desired_concurrency: Number(scaleTo) })}
            >
              scale
            </Button>
          </span>
        ) : null}
        <Button
          variant="outline"
          size="sm"
          disabled={renew.pending}
          onClick={() => void renew.mutate({ id: taskId })}
          title="Renew the owner lease"
        >
          renew
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="text-[var(--status-danger)]"
          disabled={stop.pending}
          onClick={() => setStopping(true)}
        >
          stop
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="text-[var(--status-danger)]"
          disabled={del.pending}
          onClick={() => setDeleting(true)}
        >
          delete
        </Button>
      </div>
      <RowError result={scale.result} failure={scale.failure} />
      <RowError result={stop.result} failure={stop.failure} />
      <ConfirmDialog
        open={stopping}
        onOpenChange={setStopping}
        title={`Stop task ${task.name ?? task.id ?? ""}?`}
        description="Resident pool generations are wound down; the task row stays."
        confirmLabel="Stop task"
        risk={{ tier: "standard" }}
        onConfirm={() => stop.mutateAsync({ id: taskId })}
      />
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete task ${task.name ?? task.id ?? ""}?`}
        description="The task definition and its pool are removed. This cannot be undone."
        confirmLabel="Delete task"
        risk={{ tier: "standard" }}
        onConfirm={() => del.mutateAsync({ id: taskId })}
      />
    </div>
  );
}

// RunsPanel：跨行展开的 runs 子表（按需挂载语义保留——展开挂载即取、收
// 起即卸载；批4 读面迁 kit runsResource，scope projectId+taskId 与任务详
// 情页 Runs tab 共享缓存；15s 轮询同旧拍）。
function RunsPanel({ projectId, from, taskId }: { projectId: string; from: string; taskId: string }) {
  const runs = useCollection(runsResource, {
    from,
    search: runsLookup,
    scope: { projectId, taskId },
    query: { refetchInterval: 15_000 },
  });
  const rows = runs.rows;
  return (
    <div className="bg-panel-2/30 px-3 py-3">
      {runs.status === "loading" ? (
        <Skeleton className="h-10 w-full" />
      ) : runs.error != null ? (
        <ErrorState error={new Error(describeApiError(runs.error))} onRetry={runs.refetch} />
      ) : rows.length === 0 ? (
        <p className="py-3 text-center text-xs text-muted">No runs.</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border bg-panel">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Run</TableHead>
                <TableHead>State</TableHead>
                <TableHead>Exit</TableHead>
                <TableHead>Started</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((run) => (
                <RunRow key={run.id} run={run} />
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}

function RunRow({ run }: { run: { id?: string; state?: string; status?: string; exit_code?: number; started_at?: string; created_at?: string } }) {
  const stop = useCommand(stopRunCommand);
  const [stopping, setStopping] = useState(false);
  return (
    <>
      <TableRow>
        <TableCell className="font-mono text-xs text-muted" title={run.id}>
          {shortId(run.id)}
        </TableCell>
        <TableCell className="text-xs text-muted">{run.state ?? run.status ?? "—"}</TableCell>
        <TableCell className="font-mono text-xs text-muted">{run.exit_code ?? "—"}</TableCell>
        <TableCell className="text-xs text-muted">
          <RelativeTime value={run.started_at ?? run.created_at} />
        </TableCell>
        <TableCell className="text-right">
          <Button
            variant="outline"
            size="sm"
            className="text-[var(--status-danger)]"
            disabled={stop.pending}
            onClick={() => setStopping(true)}
          >
            stop
          </Button>
        </TableCell>
      </TableRow>
      <ConfirmDialog
        open={stopping}
        onOpenChange={setStopping}
        title={`Stop run ${run.id ?? ""}?`}
        description="The running generation is terminated."
        confirmLabel="Stop run"
        risk={{ tier: "standard" }}
        onConfirm={() => stop.mutateAsync({ id: run.id ?? "" })}
      />
    </>
  );
}

// NewScheduleButton 是 Schedules tab 工具栏主创建钮——弹窗与命令自包含
//（useState 壳保留，提交面走 schedulesResource.commands.create）。
export function NewScheduleButton({ projectId }: { projectId: string }) {
  const [createOpen, setCreateOpen] = useState(false);
  // 时区默认浏览器本地时区（用户裁定 2026-10-21），检测失败回退 UTC；
  // 切换走可搜索下拉（timezone-combobox），服务端校验仍是真源。
  const [form, setForm] = useState({ name: "", cron: "0 3 * * *", timezone: detectedTimezone(), image: "", command: "", env: "", ttl: "3600" });
  const create = useCommand(schedulesResource.commands.create, {
    scope: { projectId },
    // ok 关窗（沿用旧 onSuccess 行为；无成功 toast——现句为无）
    onSettled: (result) => {
      if (result?.status === "ok") setCreateOpen(false);
    },
  });
  return (
    <>
      <Button
        size="sm"
        onClick={() => {
          setForm({ ...form, name: "", image: "" });
          setCreateOpen(true);
        }}
      >
        <PlusIcon data-icon-start-inline />
        New schedule…
      </Button>
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>New schedule</DialogTitle>
          </DialogHeader>
          <form
            className="flex flex-col gap-3"
            onSubmit={(event) => {
              event.preventDefault();
              create.mutate({
                name: form.name || undefined,
                cron: form.cron,
                timezone: form.timezone || undefined,
                image: form.image,
                command: form.command === "" ? undefined : form.command.split(/\s+/).filter((part) => part !== ""),
                env: form.env === "" ? undefined : Object.fromEntries(kvLines(form.env).map((line) => [line.slice(0, line.indexOf("=")), line.slice(line.indexOf("=") + 1)])),
                ttl_seconds: form.ttl === "" ? undefined : Number(form.ttl),
              });
            }}
          >
            <div className="grid grid-cols-2 gap-3">
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Cron</span>
                <Input value={form.cron} onChange={(event) => setForm({ ...form, cron: event.target.value })} autoFocus />
                <span className="text-2xs font-normal text-muted">5-field, in the schedule's timezone</span>
              </Label>
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Timezone</span>
                <TimezoneCombobox value={form.timezone} onChange={(timezone) => setForm({ ...form, timezone })} />
                <span className="text-2xs font-normal text-muted">IANA name</span>
              </Label>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Image</span>
                <Input value={form.image} onChange={(event) => setForm({ ...form, image: event.target.value })} />
              </Label>
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Name</span>
                <Input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} />
              </Label>
            </div>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Command</span>
              <Input value={form.command} onChange={(event) => setForm({ ...form, command: event.target.value })} />
              <span className="text-2xs font-normal text-muted">argv elements separated by spaces</span>
            </Label>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Env</span>
              <Textarea rows={2} value={form.env} onChange={(event) => setForm({ ...form, env: event.target.value })} />
              <span className="text-2xs font-normal text-muted">one KEY=VALUE per line</span>
            </Label>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">TTL seconds</span>
              <Input value={form.ttl} onChange={(event) => setForm({ ...form, ttl: event.target.value })} inputMode="numeric" />
            </Label>
            {create.result?.status === "ok" ? null : <FormError result={create.result} failure={create.failure} />}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={create.pending || form.image === "" || form.cron === ""}>
                {create.pending ? "Creating…" : "Create"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}

// SchedulesPanel：时区 cron Schedule 表 + trigger/delete。数据面
// useCollection（15s 轮询）；schedulesSearch 带独立 namespace（与 Tasks
// tab 的 q/page 互不携带——M1）。
export function SchedulesPanel({ projectId, from }: { projectId: string; from: string }) {
  const list = useCollection(schedulesResource, {
    from,
    search: schedulesSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });

  const columns: LegacyColumnDef<ScheduleEntry, any>[] = [
    {
      accessorKey: "name",
      header: "Schedule",
      meta: { sortField: "name" },
      cell: ({ row }) => (
        <div>
          <div className="font-medium">{row.original.name || shortId(row.original.id)}</div>
          <div className="font-mono text-xs text-muted" title={row.original.id}>
            {shortId(row.original.id)}
          </div>
        </div>
      ),
    },
    {
      accessorKey: "cron",
      header: "Cron",
      cell: ({ row }) => (
        <span className="font-mono text-xs">
          {row.original.cron}
          {row.original.timezone ? <span className="ml-1 text-muted">({row.original.timezone})</span> : null}
        </span>
      ),
    },
    {
      accessorKey: "image",
      header: "Image",
      meta: { sortField: "image" },
      cell: ({ row }) => <span className="font-mono text-xs text-muted">{row.original.image}</span>,
    },
    {
      // v1Schedule 的下一拍字段是 next_fire_at（旧列读 next_run_at 恒空，
      // 迁移时对齐生成类型修正）
      accessorKey: "next_fire_at",
      header: "Next",
      cell: ({ row }) => (
        <span className="text-xs text-muted">
          <RelativeTime value={row.original.next_fire_at} />
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => <ScheduleActionsCell schedule={row.original} projectId={projectId} />,
    },
  ];

  const filtered = list.query.predicate != null;
  return (
    <ListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={list} schema={tasksSearch} textPlaceholder="Filter schedules..." />
          <div className="py-2 pr-3">
            <NewScheduleButton projectId={projectId} />
          </div>
        </div>
      }
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No schedules match the filter" : "No schedules"}
      emptyDescription={
        filtered ? "Adjust or clear the filter to see rows again." : "Create a cron schedule to run tasks on a clock."
      }
      appendix={<CliEquivalent command={`fleetly schedules list --project ${projectId}`} />}
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
    </ListPage>
  );
}

// ScheduleActionsCell：trigger/delete 行内动作——批2 迁 kit 命令面（scope
// 显式注入；trigger 失效跨 tasks+schedules 双面）。
function ScheduleActionsCell({ schedule, projectId }: { schedule: { id?: string; name?: string }; projectId: string }) {
  const scope = { projectId };
  const scheduleId = schedule.id ?? "";
  const trigger = useCommand(schedulesResource.commands.trigger, { scope });
  const del = useCommand(schedulesResource.commands.delete, { scope });
  const [deleting, setDeleting] = useState(false);
  return (
    <div className="text-right">
      <div className="flex items-center justify-end gap-1">
        <Button
          variant="outline"
          size="sm"
          disabled={trigger.pending}
          onClick={() => void trigger.mutate({ id: scheduleId })}
          title="Run now (does not move the next tick)"
        >
          trigger
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="text-[var(--status-danger)]"
          disabled={del.pending}
          onClick={() => setDeleting(true)}
        >
          delete
        </Button>
      </div>
      <RowError result={trigger.result} failure={trigger.failure} />
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete schedule ${schedule.name ?? schedule.id ?? ""}?`}
        description="Fired tasks already created keep running; the clock stops."
        confirmLabel="Delete schedule"
        risk={{ tier: "standard" }}
        onConfirm={() => del.mutateAsync({ id: scheduleId })}
      />
    </div>
  );
}
