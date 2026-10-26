import { useState } from "react";
import { ClockIcon, ListRestartIcon } from "lucide-react";
import { useCommand } from "@lynx-go/uikit-core";
import type { ApiError, CommandResult } from "@lynx-go/uikit-core/kernel";
import type { components } from "@/api/automation";
import { useRuns, useSchedules, useTasks } from "@/lib/catalog";
import { ContentCrumb } from "@/components/domain/content-crumb";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { PageFrame } from "@/components/domain/page-frame";
import { PageTabs } from "@/components/domain/page-tabs";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, statusToneClass, type StatusTone } from "@/components/domain/status-badge";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toast } from "sonner";
import { describeApiError } from "@/kit/errors";
import { stopRunCommand, tasksResource } from "@/kit/tasks";
import { useLogStream } from "@/features/logs/use-log-stream";
import { LogViewer } from "@/features/logs/log-viewer";

// Task 详情（IA v3 T3b，3 tabs——工作负载三兄弟对齐）：Overview（定义/形态/
// Lease/Schedule 反链）/ Runs（列表 + Stop；run 日志按 T8 结论一期无通路，
// WaitRun 收流视图后续批）/ Settings（Scale/Stop 排空/删除）。批2 写路径
// 命令化：动作族迁 kit 命令面（读面 useTasks/useRuns/useSchedules 仍走
// catalog，失效经 legacyKeys 桥回收）；ok 沿用原 toast 句，rejected/
// conflict → result.message，failure → describeApiError。

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

export function TaskDetailPage({ projectId, taskId }: { projectId: string; taskId: string }) {
  const tasks = useTasks(projectId);
  const task = (tasks.data ?? []).find((entry) => entry.id === taskId);
  const schedules = useSchedules(projectId);
  // 来源 Schedule 反链：schedule.last_task_id = 最近一拍铸出的 Task。
  const sourceSchedule = (schedules.data ?? []).find((schedule) => schedule.last_task_id === taskId);
  const [tab, setTab] = useState<"overview" | "runs" | "settings">("overview");

  return (
    <PageFrame
      breadcrumb={
        <ContentCrumb
          projectId={projectId}
          section={{ label: "Tasks", to: "/p/$projectId/tasks" }}
          current={task?.name ?? taskId}
        />
      }
    >
      {/* 英雄卡对齐 App Detail（2026-10-12 走查：裸标题与 Apps 形态脱队——
          头像/名称/徽标/可读 id 统一收卡）。 */}
      <section className="mb-5 flex flex-wrap items-center gap-4 rounded-xl border bg-card p-5">
        <ProjectAvatar seed={taskId} label={task?.name ?? taskId} className="size-11 rounded-xl text-base" />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="font-heading text-xl font-semibold tracking-tight">{task?.name ?? taskId}</h1>
            <StatusBadge tone={taskTone(task?.state)}>{task?.state ?? "unknown"}</StatusBadge>
            {task?.form ? (
              <span className="rounded-full border px-2 py-0.5 text-2xs font-semibold text-muted-foreground">{task.form}</span>
            ) : null}
          </div>
          <div className="flex items-center gap-1 font-mono text-2xs text-muted-foreground">
            {taskId}
            <CopyButton value={taskId} />
          </div>
        </div>
      </section>

      <PageTabs
        tabs={[
          { value: "overview", label: "Overview" },
          { value: "runs", label: "Runs" },
          { value: "settings", label: "Settings" },
        ]}
        current={tab}
        onChange={setTab}
      />
      {tab === "overview" ? (
        <TaskOverview task={task} sourceScheduleId={sourceSchedule?.id} sourceScheduleName={sourceSchedule?.name} sourceScheduleCron={sourceSchedule?.cron} />
      ) : null}
      {tab === "runs" ? <TaskRuns taskId={taskId} /> : null}
      {tab === "settings" ? <TaskSettings task={task} projectId={projectId} taskId={taskId} /> : null}
    </PageFrame>
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
        <h3 className="mb-3 text-sm font-semibold">Definition</h3>
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
        <h3 className="mb-3 text-sm font-semibold">Pool &amp; lease</h3>
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
        <h3 className="mb-3 text-sm font-semibold">Timing &amp; source</h3>
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
                <span className="text-2xs text-muted-foreground">last beat spawned this task</span>
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
            <thead className="border-b bg-panel-2/40 text-left text-2xs font-semibold tracking-wide text-muted-foreground uppercase">
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
      <p className="text-2xs text-muted-foreground">
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
    log.start({ runId: run.id ?? "", processes: [], containers: [], tailLines: "300", text: "", follow: true });
  }
  const stop = useCommand(stopRunCommand, {
    // ok 原句保留；rejected/conflict → result.message；failure → describeApiError
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        toast("Stop requested");
      } else if (result != null) {
        toast.error(result.message);
      } else if (failure != null) {
        toast.error(describeApiError(failure));
      }
    },
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
          disabled={!active || stop.pending}
          onClick={() => stop.mutate({ id: run.id ?? "" })}
          title="Request a graceful stop"
        >
          Stop
        </Button>
      </td>
    </tr>
      {logsOpen ? (
        <tr className="border-b last:border-b-0 bg-panel-2/30">
          <td colSpan={6} className="px-3 py-2">
            <div className="h-64 overflow-hidden rounded-lg border">
              <LogViewer frames={log.frames} streaming={log.streaming} />
            </div>
            <div className="mt-1.5 flex items-center gap-3 font-mono text-2xs text-muted-foreground">
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
  const [scaleTo, setScaleTo] = useState("");
  const scope = { projectId };
  // ok 沿用原 toast 句；结果两分错误面（rejected/conflict → message，
  // failure → describeApiError）。失效由命令 descriptor 承载（kit selector
  // + legacyKeys 桥），不再手握 queryClient。
  const settleToast =
    (okMessage: string) =>
    (result: CommandResult<unknown> | null, failure: ApiError | null): void => {
      if (result?.status === "ok") {
        toast(okMessage);
      } else if (result != null) {
        toast.error(result.message);
      } else if (failure != null) {
        toast.error(describeApiError(failure));
      }
    };
  const scale = useCommand(tasksResource.commands.scale, {
    scope,
    onSettled: settleToast("Scale updated — converges on the next reconcile"),
  });
  const stop = useCommand(tasksResource.commands.stop, {
    scope,
    onSettled: settleToast("Drain requested — runs wind down to their terminal states"),
  });
  const renew = useCommand(tasksResource.commands.renew, {
    scope,
    onSettled: settleToast("Owner lease renewed"),
  });
  const remove = useCommand(tasksResource.commands.delete, {
    scope,
    onSettled: settleToast("Task deleted"),
  });
  return (
    <div className="flex flex-col gap-4">
      <section className="rounded-xl border bg-card p-5">
        <h3 className="mb-3 text-sm font-semibold">Pool operations</h3>
        <div className="flex flex-wrap items-end gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="task-scale" className="text-2xs text-muted-foreground">
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
          <Button size="sm" disabled={scale.pending || scaleTo === ""} onClick={() => scale.mutate({ id: taskId, desired_concurrency: Number(scaleTo) })}>
            Scale pool
          </Button>
          <Button size="sm" variant="outline" disabled={renew.pending} onClick={() => renew.mutate({ id: taskId })} title="Renew the owner lease (proves ownership presence)">
            Renew lease
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={stop.pending || task?.state !== "active"}
            onClick={() => stop.mutate({ id: taskId, force: false })}
            title="Stop feeding new runs; existing runs finish naturally (TTL/completion)"
          >
            Drain
          </Button>
          <Button size="sm" variant="outline" disabled={stop.pending || task?.state !== "active"} onClick={() => stop.mutate({ id: taskId, force: true })} title="Graceful stop with StopGrace — running runs are wound down">
            Force stop…
          </Button>
        </div>
      </section>
      <section className="rounded-xl border border-destructive/30 bg-card p-5">
        <h3 className="mb-2 text-sm font-semibold text-destructive">Danger zone</h3>
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
                disabled={remove.pending}
                onClick={(event) => {
                  event.preventDefault();
                  remove.mutate({ id: taskId });
                }}
              >
                {remove.pending ? "Deleting…" : "Delete"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </section>
    </div>
  );
}
