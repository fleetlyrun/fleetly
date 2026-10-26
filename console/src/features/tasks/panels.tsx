import { useState } from "react";
import { PlusIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { useRuns, useSchedules, useTasks } from "@/lib/catalog";
import { useApiMutation } from "@/lib/use-api-mutation";
import { describeError } from "@/lib/api-errors";
import { shortId } from "@/lib/format";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { EmptyState } from "@/components/domain/empty-state";
import { ErrorState } from "@/components/domain/error-state";
import { ListCard } from "@/components/domain/list-page";
import { RelativeTime } from "@/components/domain/relative-time";
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

// Tasks 面板族（UI v2 批 4 → 模板化收编 ListCard）：Task 双形态
//（one-shot/resident 池）+ Run 观察 + 时区 cron Schedule。动词面对齐 CLI
//（create/scale/stop/delete/renew + schedules create/trigger/delete +
// runs stop）。项目语境改为入参。Tasks 表的 runs 跨行展开走 children
// 渲染槽；表格四态/工具栏/分页/CLI 行收口 ListCard。pre-v2 共享层脱钩。

// kvLines 文本域 → 重复旗标面（K=V 行）。
function kvLines(text: string): Array<string> {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "" && !line.startsWith("#"));
}

// FormError 是弹窗表单的提交错误面（danger 信封盒，describeError 单源）。
function FormError({ error }: { error: unknown }) {
  return (
    <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 font-mono text-2xs break-all text-muted-foreground">
      {describeError(error).detail}
    </div>
  );
}

// RowError 是行内动作失败的披露行（v2 口径：分状态文案单源）。
function RowError({ error }: { error: unknown }) {
  const described = describeError(error);
  return (
    <div className="mt-1 text-2xs text-[var(--status-danger)]">
      {described.title} — <span className="font-mono">{described.detail}</span>
    </div>
  );
}

// NewTaskButton 是 Tasks 工具栏主创建钮——弹窗与 mutation 自包含。
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
  const create = useApiMutation({
    path: "/v1/tasks",
    method: "POST",
    body: () => ({
      project_id: projectId,
      name: form.name || undefined,
      image: form.image,
      form: form.form === "resident" ? "resident" : undefined,
      concurrency: form.form === "resident" ? Number(form.concurrency) : undefined,
      ttl_seconds: form.ttl === "" ? undefined : Number(form.ttl),
      network_group: form.networkGroup || undefined,
      command: form.command === "" ? undefined : form.command.split(/\s+/).filter((part) => part !== ""),
      env: form.env === "" ? undefined : Object.fromEntries(kvLines(form.env).map((line) => [line.slice(0, line.indexOf("=")), line.slice(line.indexOf("=") + 1)])),
      secret_refs: form.secretRefs === "" ? undefined : form.secretRefs.split(/[,\s]+/).filter((part) => part !== ""),
      cpu_millis: form.cpuMillis === "" ? undefined : Number(form.cpuMillis),
      memory_mb: form.memoryMb === "" ? undefined : Number(form.memoryMb),
    }),
    invalidate: [["resources", "tasks", projectId]],
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
              create.mutate(undefined, { onSuccess: () => setCreateOpen(false) });
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
                <span className="text-2xs font-normal text-muted-foreground">optional</span>
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
                <span className="text-2xs font-normal text-muted-foreground">lease lifetime</span>
              </Label>
            </div>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Network group</span>
              <Input value={form.networkGroup} onChange={(event) => setForm({ ...form, networkGroup: event.target.value })} placeholder="dispatcher" />
              <span className="text-2xs font-normal text-muted-foreground">optional dedicated network group (cross-attach via taskGroup:&lt;name&gt;)</span>
            </Label>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Command</span>
              <Input value={form.command} onChange={(event) => setForm({ ...form, command: event.target.value })} placeholder="sh -c migrate up" />
              <span className="text-2xs font-normal text-muted-foreground">argv elements separated by spaces</span>
            </Label>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Env</span>
              <Textarea rows={2} value={form.env} onChange={(event) => setForm({ ...form, env: event.target.value })} />
              <span className="text-2xs font-normal text-muted-foreground">one KEY=VALUE per line</span>
            </Label>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Secret refs</span>
              <Input value={form.secretRefs} onChange={(event) => setForm({ ...form, secretRefs: event.target.value })} />
              <span className="text-2xs font-normal text-muted-foreground">comma-separated project secret names</span>
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
            {create.isError ? <FormError error={create.error} /> : null}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending || form.image === ""}>
                {create.isPending ? "Creating…" : "Create"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}

// TasksPanel：Task 表（runs 跨行展开）+ scale/stop/renew/delete。
export function TasksPanel({ projectId }: { projectId: string }) {
  return <TasksTab projectId={projectId} />;
}

function TasksTab({ projectId }: { projectId: string }) {
  const tasks = useTasks(projectId);
  return (
    <section className="flex flex-col gap-3">
      <ListCard
        label="tasks"
        placeholder="Filter tasks..."
        rows={tasks.data ?? []}
        fieldsOf={(task) => [task.name ?? "", task.id ?? "", task.form ?? "", task.state ?? ""]}
        loading={tasks.isPending}
        error={tasks.isError ? tasks.error : null}
        onRetry={() => void tasks.refetch()}
        cli={`fleetly tasks list --project ${projectId}`}
        actions={<NewTaskButton projectId={projectId} />}
        empty={
          projectId === "" ? (
            <EmptyState icon={PlusIcon} title="Enter a project id" description="Pick a project to list its tasks." />
          ) : (
            <EmptyState icon={PlusIcon} title="No tasks" description="Create a one-shot task or a resident pool." />
          )
        }
      >
        {({ pageRows }) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Task</TableHead>
                <TableHead>Form</TableHead>
                <TableHead>State</TableHead>
                <TableHead>Image</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {pageRows.map((task) => (
                <TaskRow key={task.id} task={task} />
              ))}
            </TableBody>
          </Table>
        )}
      </ListCard>
    </section>
  );
}

function TaskRow({ task }: { task: { id?: string; name?: string; form?: string; state?: string; status?: string; image?: string; project_id?: string } }) {
  const [expanded, setExpanded] = useState(false);
  const runs = useRuns(expanded ? task.id ?? "" : "");
  const [scaleTo, setScaleTo] = useState("");
  const [stopping, setStopping] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const scale = useApiMutation({
    path: `/v1/tasks/${encodeURIComponent(task.id ?? "")}/scale`,
    method: "POST",
    body: () => ({ concurrency: Number(scaleTo) }),
    invalidate: [["resources", "tasks"], ["resources", "runs", task.id]],
  });
  const stop = useApiMutation({
    path: `/v1/tasks/${encodeURIComponent(task.id ?? "")}/stop`,
    method: "POST",
    body: () => ({}),
    invalidate: [["resources", "tasks"], ["resources", "runs", task.id]],
  });
  const renew = useApiMutation({
    path: `/v1/tasks/${encodeURIComponent(task.id ?? "")}/renew`,
    method: "POST",
    body: () => ({}),
  });
  const del = useApiMutation({
    path: `/v1/tasks/${encodeURIComponent(task.id ?? "")}`,
    method: "DELETE",
    invalidate: [["resources", "tasks"]],
  });
  return (
    <>
      <TableRow>
        <TableCell>
          <div className="font-medium">
            <a
              href={`/p/${encodeURIComponent(task.project_id ?? "")}/tasks/${encodeURIComponent(task.id ?? "")}`}
              className="hover:underline"
            >
              {task.name || shortId(task.id)}
            </a>
          </div>
          <div className="font-mono text-xs text-muted-foreground" title={task.id}>
            {shortId(task.id)}
          </div>
        </TableCell>
        <TableCell className="text-xs text-muted-foreground">{task.form}</TableCell>
        <TableCell className="text-xs text-muted-foreground">{task.state ?? task.status ?? "—"}</TableCell>
        <TableCell className="font-mono text-xs text-muted-foreground">{task.image}</TableCell>
        <TableCell>
          <div className="flex flex-wrap items-center justify-end gap-1">
            <Button variant="outline" size="sm" onClick={() => setExpanded((prev) => !prev)}>
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
                <Button variant="outline" size="sm" disabled={scale.isPending || scaleTo === ""} onClick={() => void scale.mutate()}>
                  scale
                </Button>
              </span>
            ) : null}
            <Button variant="outline" size="sm" disabled={renew.isPending} onClick={() => void renew.mutate()} title="Renew the owner lease">
              renew
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="text-[var(--status-danger)]"
              disabled={stop.isPending}
              onClick={() => setStopping(true)}
            >
              stop
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="text-[var(--status-danger)]"
              disabled={del.isPending}
              onClick={() => setDeleting(true)}
            >
              delete
            </Button>
          </div>
          {scale.isError ? <RowError error={scale.error} /> : null}
          {stop.isError ? <RowError error={stop.error} /> : null}
        </TableCell>
      </TableRow>
      {expanded ? (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={5} className="bg-panel-2/30">
            {runs.isPending ? (
              <Skeleton className="h-10 w-full" />
            ) : runs.isError ? (
              <ErrorState error={runs.error} onRetry={() => void runs.refetch()} />
            ) : (runs.data ?? []).length === 0 ? (
              <p className="py-3 text-center text-xs text-muted-foreground">No runs.</p>
            ) : (
              <div className="overflow-x-auto rounded-lg border bg-card">
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
                    {(runs.data ?? []).map((run) => (
                      <RunRow key={run.id} run={run} />
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
          </TableCell>
        </TableRow>
      ) : null}
      <ConfirmDialog
        open={stopping}
        onOpenChange={setStopping}
        title={`Stop task ${task.name ?? task.id ?? ""}?`}
        description="Resident pool generations are wound down; the task row stays."
        confirmLabel="Stop task"
        onConfirm={() => stop.mutateAsync()}
      />
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete task ${task.name ?? task.id ?? ""}?`}
        description="The task definition and its pool are removed. This cannot be undone."
        confirmLabel="Delete task"
        onConfirm={() => del.mutateAsync()}
      />
    </>
  );
}

function RunRow({ run }: { run: { id?: string; state?: string; status?: string; exit_code?: number; started_at?: string; created_at?: string } }) {
  const stop = useApiMutation({
    path: `/v1/runs/${encodeURIComponent(run.id ?? "")}/stop`,
    method: "POST",
    body: () => ({}),
    invalidate: [["resources", "runs"]],
  });
  const [stopping, setStopping] = useState(false);
  return (
    <>
      <TableRow>
        <TableCell className="font-mono text-xs text-muted-foreground" title={run.id}>
          {shortId(run.id)}
        </TableCell>
        <TableCell className="text-xs text-muted-foreground">{run.state ?? run.status ?? "—"}</TableCell>
        <TableCell className="font-mono text-xs text-muted-foreground">{run.exit_code ?? "—"}</TableCell>
        <TableCell className="text-xs text-muted-foreground">
          <RelativeTime value={run.started_at ?? run.created_at} />
        </TableCell>
        <TableCell className="text-right">
          <Button
            variant="outline"
            size="sm"
            className="text-[var(--status-danger)]"
            disabled={stop.isPending}
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
        onConfirm={() => stop.mutateAsync()}
      />
    </>
  );
}

// NewScheduleButton 是 Schedules tab 工具栏主创建钮——弹窗与 mutation
// 自包含。
export function NewScheduleButton({ projectId }: { projectId: string }) {
  const [createOpen, setCreateOpen] = useState(false);
  // 时区默认浏览器本地时区（用户裁定 2026-10-21），检测失败回退 UTC；
  // 切换走可搜索下拉（timezone-combobox），服务端校验仍是真源。
  const [form, setForm] = useState({ name: "", cron: "0 3 * * *", timezone: detectedTimezone(), image: "", command: "", env: "", ttl: "3600" });
  const create = useApiMutation({
    path: "/v1/schedules",
    method: "POST",
    body: () => ({
      project_id: projectId,
      name: form.name || undefined,
      cron: form.cron,
      timezone: form.timezone || undefined,
      image: form.image,
      command: form.command === "" ? undefined : form.command.split(/\s+/).filter((part) => part !== ""),
      env: form.env === "" ? undefined : Object.fromEntries(kvLines(form.env).map((line) => [line.slice(0, line.indexOf("=")), line.slice(line.indexOf("=") + 1)])),
      ttl_seconds: form.ttl === "" ? undefined : Number(form.ttl),
    }),
    invalidate: [["resources", "schedules", projectId]],
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
              create.mutate(undefined, { onSuccess: () => setCreateOpen(false) });
            }}
          >
            <div className="grid grid-cols-2 gap-3">
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Cron</span>
                <Input value={form.cron} onChange={(event) => setForm({ ...form, cron: event.target.value })} autoFocus />
                <span className="text-2xs font-normal text-muted-foreground">5-field, in the schedule's timezone</span>
              </Label>
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Timezone</span>
                <TimezoneCombobox value={form.timezone} onChange={(timezone) => setForm({ ...form, timezone })} />
                <span className="text-2xs font-normal text-muted-foreground">IANA name</span>
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
              <span className="text-2xs font-normal text-muted-foreground">argv elements separated by spaces</span>
            </Label>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Env</span>
              <Textarea rows={2} value={form.env} onChange={(event) => setForm({ ...form, env: event.target.value })} />
              <span className="text-2xs font-normal text-muted-foreground">one KEY=VALUE per line</span>
            </Label>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">TTL seconds</span>
              <Input value={form.ttl} onChange={(event) => setForm({ ...form, ttl: event.target.value })} inputMode="numeric" />
            </Label>
            {create.isError ? <FormError error={create.error} /> : null}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending || form.image === "" || form.cron === ""}>
                {create.isPending ? "Creating…" : "Create"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}

// SchedulesPanel：时区 cron Schedule 表 + trigger/delete。
export function SchedulesPanel({ projectId }: { projectId: string }) {
  return <SchedulesTab projectId={projectId} />;
}

function SchedulesTab({ projectId }: { projectId: string }) {
  const schedules = useSchedules(projectId);

  const columns: LegacyColumnDef<{ id?: string; name?: string; cron?: string; timezone?: string; image?: string; next_run_at?: string }, any>[] = [
    {
      accessorKey: "name",
      header: "Schedule",
      cell: ({ row }) => (
        <div>
          <div className="font-medium">{row.original.name || shortId(row.original.id)}</div>
          <div className="font-mono text-xs text-muted-foreground" title={row.original.id}>
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
          {row.original.timezone ? <span className="ml-1 text-muted-foreground">({row.original.timezone})</span> : null}
        </span>
      ),
    },
    {
      accessorKey: "image",
      header: "Image",
      cell: ({ row }) => <span className="font-mono text-xs text-muted-foreground">{row.original.image}</span>,
    },
    {
      accessorKey: "next_run_at",
      header: "Next",
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
          <RelativeTime value={row.original.next_run_at} />
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => <ScheduleActionsCell schedule={row.original} />,
    },
  ];

  return (
    <section className="flex flex-col gap-3">
      <ListCard
        label="schedules"
        placeholder="Filter schedules..."
        rows={schedules.data ?? []}
        fieldsOf={(schedule) => [schedule.name ?? "", schedule.cron ?? "", schedule.image ?? ""]}
        loading={schedules.isPending}
        error={schedules.isError ? schedules.error : null}
        onRetry={() => void schedules.refetch()}
        columns={columns}
        cli={`fleetly schedules list --project ${projectId}`}
        actions={<NewScheduleButton projectId={projectId} />}
        empty={
          projectId === "" ? (
            <EmptyState icon={PlusIcon} title="Enter a project id" description="Pick a project to list its schedules." />
          ) : (
            <EmptyState icon={PlusIcon} title="No schedules" description="Create a cron schedule to run tasks on a clock." />
          )
        }
      />
    </section>
  );
}

function ScheduleActionsCell({ schedule }: { schedule: { id?: string; name?: string } }) {
  const trigger = useApiMutation({
    path: `/v1/schedules/${encodeURIComponent(schedule.id ?? "")}/trigger`,
    method: "POST",
    body: () => ({}),
  });
  const del = useApiMutation({
    path: `/v1/schedules/${encodeURIComponent(schedule.id ?? "")}`,
    method: "DELETE",
    invalidate: [["resources", "schedules"]],
  });
  const [deleting, setDeleting] = useState(false);
  return (
    <div className="text-right">
      <div className="flex items-center justify-end gap-1">
        <Button variant="outline" size="sm" disabled={trigger.isPending} onClick={() => void trigger.mutate()} title="Run now (does not move the next tick)">
          trigger
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="text-[var(--status-danger)]"
          disabled={del.isPending}
          onClick={() => setDeleting(true)}
        >
          delete
        </Button>
      </div>
      {trigger.isError ? <RowError error={trigger.error} /> : null}
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete schedule ${schedule.name ?? schedule.id ?? ""}?`}
        description="Fired tasks already created keep running; the clock stops."
        confirmLabel="Delete schedule"
        onConfirm={() => del.mutateAsync()}
      />
    </div>
  );
}
