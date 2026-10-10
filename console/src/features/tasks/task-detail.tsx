import { useState } from "react";
import { ClockIcon, ListRestartIcon } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { apiSend } from "@/api/client";
import type { components } from "@/api/automation";
import { useRuns, useSchedules, useTasks } from "@/lib/catalog";
import { ContentCrumb } from "@/components/domain/content-crumb";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { PageHeader } from "@/components/domain/page-header";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, statusToneClass, type StatusTone } from "@/components/domain/status-badge";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { toast } from "sonner";
import { useLogStream } from "@/features/logs/use-log-stream";
import { LogViewer } from "@/features/logs/log-viewer";

// Task 详情（IA v3 T3b，3 tabs——工作负载三兄弟对齐）：Overview（定义/形态/
// Lease/Schedule 反链）/ Runs（列表 + Stop；run 日志按 T8 结论一期无通路，
// WaitRun 收流视图后续批）/ Settings（Scale/Stop 排空/删除）。

export type TaskEntry = components["schemas"]["v1Task"];
type RunEntry = components["schemas"]["v1Run"];

function taskTone(state: string | undefined): StatusTone {
  switch (state) {
    case "active":
      return "success";
    case "draining":
      return "warning";
    case "failed":
      return "danger";
    default:
      return "neutral";
  }
}

function runTone(state: string | undefined): StatusTone {
  switch (state) {
    case "running":
      return "info";
    case "pending":
      return "neutral";
    case "stopping":
      return "warning";
    case "failed":
      return "danger";
    default:
      return "success";
  }
}

function fieldError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function TaskDetailPage({ projectId, taskId }: { projectId: string; taskId: string }) {
  const tasks = useTasks(projectId);
  const task = (tasks.data ?? []).find((entry) => entry.id === taskId);
  const schedules = useSchedules(projectId);
  // 来源 Schedule 反链：schedule.last_task_id = 最近一拍铸出的 Task。
  const sourceSchedule = (schedules.data ?? []).find((schedule) => schedule.last_task_id === taskId);

  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader
        breadcrumb={
          <ContentCrumb
            projectId={projectId}
            section={{ label: "Tasks", to: "/p/$projectId/tasks" }}
            current={task?.name ?? taskId}
          />
        }
        title={
          <span className="flex items-center gap-3">
            <ProjectAvatar seed={taskId} label={task?.name ?? taskId} className="size-8 rounded-xl text-sm" />
            {task?.name ?? taskId}
            <StatusBadge tone={taskTone(task?.state)}>{task?.state ?? "unknown"}</StatusBadge>
            {task?.form ? (
              <span className="rounded-full border px-2 py-0.5 text-[11.5px] font-semibold text-muted-foreground">{task.form}</span>
            ) : null}
          </span>
        }
        description={
          <span className="flex items-center gap-1 font-mono text-[11.5px]">
            {taskId}
            <CopyButton value={taskId} />
          </span>
        }
      />

      <Tabs defaultValue="overview">
        <TabsList className="mb-4">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="runs">Runs</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>
        <TabsContent value="overview">
          <TaskOverview task={task} sourceScheduleId={sourceSchedule?.id} sourceScheduleName={sourceSchedule?.name} sourceScheduleCron={sourceSchedule?.cron} />
        </TabsContent>
        <TabsContent value="runs">
          <TaskRuns taskId={taskId} />
        </TabsContent>
        <TabsContent value="settings">
          <TaskSettings task={task} projectId={projectId} taskId={taskId} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function TaskOverview({
  task,
  sourceScheduleId,
  sourceScheduleName,
  sourceScheduleCron,
}: {
  task: TaskEntry | undefined;
  sourceScheduleId?: string;
  sourceScheduleName?: string;
  sourceScheduleCron?: string;
}) {
  if (task == null) {
    return <EmptyState icon={ClockIcon} title="Task not found" description="It may have been deleted." />;
  }
  return (
    <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <section className="rounded-xl border bg-card p-4">
        <h3 className="mb-3 text-[13px] font-semibold">Definition</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted-foreground">image</dt>
          <dd className="break-all font-mono">{task.image ?? "—"}</dd>
          <dt className="text-muted-foreground">command</dt>
          <dd className="font-mono">{task.command?.length ? task.command.join(" ") : "—"}</dd>
          <dt className="text-muted-foreground">network group</dt>
          <dd className="font-mono">{task.network_group || "—"}</dd>
          <dt className="text-muted-foreground">owner token</dt>
          <dd className="font-mono">{task.owner_token_id ? `${task.owner_token_id.slice(0, 12)}…` : "—"}</dd>
        </dl>
      </section>
      <section className="rounded-xl border bg-card p-4">
        <h3 className="mb-3 text-[13px] font-semibold">Pool &amp; lease</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted-foreground">form</dt>
          <dd className="font-mono">{task.form ?? "—"}</dd>
          <dt className="text-muted-foreground">desired concurrency</dt>
          <dd className="font-mono">{task.desired_concurrency ?? "—"}</dd>
          <dt className="text-muted-foreground">active runs</dt>
          <dd className="font-mono">{task.active_run_count ?? "0"}</dd>
          <dt className="text-muted-foreground">ttl</dt>
          <dd className="font-mono">{task.ttl_seconds ? `${task.ttl_seconds}s` : "—"}</dd>
          <dt className="text-muted-foreground">lease deadline</dt>
          <dd>
            <RelativeTime value={task.lease_deadline} />
          </dd>
        </dl>
      </section>
      <section className="rounded-xl border bg-card p-4">
        <h3 className="mb-3 text-[13px] font-semibold">Timing &amp; source</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted-foreground">dns</dt>
          <dd className="break-all font-mono">{task.dns_name || "—"}</dd>
          <dt className="text-muted-foreground">created</dt>
          <dd>
            <RelativeTime value={task.created_at} />
          </dd>
          <dt className="text-muted-foreground">updated</dt>
          <dd>
            <RelativeTime value={task.updated_at} />
          </dd>
          <dt className="text-muted-foreground">finished</dt>
          <dd>
            <RelativeTime value={task.finished_at} />
          </dd>
          <dt className="text-muted-foreground">schedule</dt>
          <dd>
            {sourceScheduleId ? (
              <span className="flex flex-col">
                <span className="font-mono">
                  {sourceScheduleName || sourceScheduleId}
                  {sourceScheduleCron ? ` · ${sourceScheduleCron}` : ""}
                </span>
                <span className="text-[11px] text-muted-foreground">last beat spawned this task</span>
              </span>
            ) : (
              <span className="text-muted-foreground">—</span>
            )}
          </dd>
        </dl>
      </section>
    </div>
  );
}

function TaskRuns({ taskId }: { taskId: string }) {
  const runs = useRuns(taskId);
  const rows = runs.data ?? [];
  return (
    <div className="flex flex-col gap-3">
      {runs.isPending ? (
        <p className="py-10 text-center text-xs text-muted-foreground">Loading runs…</p>
      ) : rows.length === 0 ? (
        <EmptyState icon={ListRestartIcon} title="No runs yet" description="Runs appear here as the pool (or a schedule beat) spawns them." />
      ) : (
        <div className="overflow-hidden rounded-xl border">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/40 text-left text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
              <tr>
                <th className="px-3 py-2">Run</th>
                <th className="px-3 py-2">State</th>
                <th className="px-3 py-2">Exit</th>
                <th className="px-3 py-2">Stop reason</th>
                <th className="px-3 py-2">Created</th>
                <th className="px-3 py-2 text-right">Actions</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((run) => (
                <RunRow key={run.id} run={run} />
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="text-[11.5px] text-muted-foreground">
        Run logs need the database/run addressing axis (IA v3 §8 二期 proto) — states and stop reasons are shown here until then.
      </p>
    </div>
  );
}

function RunRow({ run }: { run: RunEntry }) {
  const [logsOpen, setLogsOpen] = useState(false);
  const log = useLogStream();
  function toggleLogs() {
    if (logsOpen) {
      log.stop();
      setLogsOpen(false);
      return;
    }
    setLogsOpen(true);
    log.start({ runId: run.id ?? "", process: "", tailLines: "300", text: "", follow: true });
  }
  const stop = useMutation({
    mutationFn: async () => apiSend(`/v1/runs/${encodeURIComponent(run.id ?? "")}/stop`, "POST", {}),
    onSuccess: () => toast("Stop requested"),
    onError: (cause) => toast.error(fieldError(cause)),
  });
  const active = run.state === "running" || run.state === "pending" || run.state === "stopping";
  return (
    <>
      <tr className="border-b last:border-b-0">
      <td className="px-3 py-2">
        <span className="flex items-center gap-1 font-mono text-xs">
          {(run.id ?? "").slice(0, 14)}…
          <CopyButton value={run.id ?? ""} />
        </span>
      </td>
      <td className="px-3 py-2">
        <span className={`inline-flex items-center gap-1.5 text-xs font-medium ${statusToneClass(runTone(run.state))}`}>
          <span className={`inline-block size-1.5 rounded-full bg-current ${active ? "animate-pulse" : ""}`} />
          {run.state ?? "unknown"}
        </span>
      </td>
      <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
        {run.exit_code === 0 || (run.exit_code != null && run.exit_code !== 0) ? run.exit_code : "—"}
      </td>
      <td className="px-3 py-2 text-xs text-muted-foreground">{run.stop_reason ?? "—"}</td>
      <td className="px-3 py-2 text-xs text-muted-foreground">
        <RelativeTime value={run.created_at} />
      </td>
      <td className="px-3 py-2 text-right">
        <Button variant="outline" size="sm" onClick={toggleLogs}>
          {logsOpen ? "Hide logs" : "Logs"}
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="ml-1.5"
          disabled={!active || stop.isPending}
          onClick={() => stop.mutate()}
          title="Request a graceful stop"
        >
          Stop
        </Button>
      </td>
    </tr>
      {logsOpen ? (
        <tr className="border-b last:border-b-0 bg-muted/30">
          <td colSpan={6} className="px-3 py-2">
            <div className="h-64 overflow-hidden rounded-lg border">
              <LogViewer frames={log.frames} streaming={log.streaming} />
            </div>
            <div className="mt-1.5 flex items-center gap-3 font-mono text-[11px] text-muted-foreground">
              <span>scope: run {run.id}</span>
              <span>{log.frames.length} frames</span>
              {log.error != null ? (
                <span className="text-destructive">{log.error instanceof Error ? log.error.message : String(log.error)}</span>
              ) : null}
            </div>
          </td>
        </tr>
      ) : null}
    </>
  );
}

function TaskSettings({ task, projectId, taskId }: { task: TaskEntry | undefined; projectId: string; taskId: string }) {
  const queryClient = useQueryClient();
  const [scaleTo, setScaleTo] = useState("");
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["resources", "tasks", projectId] });
  const scale = useMutation({
    mutationFn: async () => apiSend(`/v1/tasks/${encodeURIComponent(taskId)}/scale`, "POST", { desired_concurrency: scaleTo }),
    onSuccess: () => {
      toast("Scale updated — converges on the next reconcile");
      invalidate();
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  const stop = useMutation({
    mutationFn: async (force: boolean) => apiSend(`/v1/tasks/${encodeURIComponent(taskId)}/stop`, "POST", { force }),
    onSuccess: () => {
      toast("Drain requested — runs wind down to their terminal states");
      invalidate();
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  const renew = useMutation({
    mutationFn: async () => apiSend(`/v1/tasks/${encodeURIComponent(taskId)}/renew`, "POST", {}),
    onSuccess: () => {
      toast("Owner lease renewed");
      invalidate();
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  const remove = useMutation({
    mutationFn: async () => apiSend(`/v1/tasks/${encodeURIComponent(taskId)}`, "DELETE"),
    onSuccess: () => {
      toast("Task deleted");
      invalidate();
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  return (
    <div className="flex flex-col gap-4">
      <section className="max-w-2xl rounded-xl border bg-card p-5">
        <h3 className="mb-3 text-[13px] font-semibold">Pool operations</h3>
        <div className="flex flex-wrap items-end gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="task-scale" className="text-[11px] text-muted-foreground">
              Desired concurrency
            </Label>
            <Input
              id="task-scale"
              className="h-8 w-24 font-mono text-xs"
              inputMode="numeric"
              value={scaleTo}
              onChange={(event) => setScaleTo(event.target.value.replace(/\D/g, ""))}
              placeholder={String(task?.desired_concurrency ?? "")}
            />
          </div>
          <Button size="sm" disabled={scale.isPending || scaleTo === ""} onClick={() => scale.mutate()}>
            Scale pool
          </Button>
          <Button size="sm" variant="outline" disabled={renew.isPending} onClick={() => renew.mutate()} title="Renew the owner lease (proves ownership presence)">
            Renew lease
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={stop.isPending || task?.state !== "active"}
            onClick={() => stop.mutate(false)}
            title="Stop feeding new runs; existing runs finish naturally (TTL/completion)"
          >
            Drain
          </Button>
          <Button size="sm" variant="outline" disabled={stop.isPending || task?.state !== "active"} onClick={() => stop.mutate(true)} title="Graceful stop with StopGrace — running runs are wound down">
            Force stop…
          </Button>
        </div>
      </section>
      <section className="max-w-2xl rounded-xl border border-destructive/30 bg-card p-5">
        <h3 className="mb-2 text-[13px] font-semibold text-destructive">Danger zone</h3>
        <p className="mb-4 text-xs text-muted-foreground">
          Deleting the task removes its definition and pool. Terminal runs stay in the ledger; active runs are drained first.
        </p>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="destructive" size="sm" disabled={task == null}>
              Delete task…
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Delete task {task?.name}?</AlertDialogTitle>
              <AlertDialogDescription>
                The task definition and pool are removed. Active runs are drained; terminal runs remain in the ledger.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                disabled={remove.isPending}
                onClick={(event) => {
                  event.preventDefault();
                  remove.mutate();
                }}
              >
                {remove.isPending ? "Deleting…" : "Delete"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </section>
    </div>
  );
}
