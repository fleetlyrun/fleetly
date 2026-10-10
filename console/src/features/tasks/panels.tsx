import { useState } from "react";
import { PlusIcon } from "lucide-react";
import { useRuns, useSchedules, useTasks } from "@/lib/catalog";
import { CliEquivalent, ListPagination, ListToolbar, useClientPage, useListFilter } from "@/components/domain/list-toolbar";
import { Button } from "@/components/ui/button";
import {
  DangerRowButton,
  EmptyNote,
  ErrorNote,
  Field,
  LoadingNote,
  Modal,
  MutationBanner,
  PrimaryButton,
  RowButton,
  Select,
  TableHead,
  TableWrap,
  TextArea,
  TextInput,
  formatTime,
  shortId,
  useApiMutation,
} from "@/components/ui";

// Tasks 面板族（UI v2 批 4）：自旧 Tasks.tsx 原样搬迁——Task 双形态
// （one-shot/resident 池）+ Run 观察 + 时区 cron Schedule。动词面对齐 CLI
//（create/scale/stop/delete/renew + schedules create/trigger/delete +
// runs stop）。项目语境改为入参——手贴 project ID 的交互退场（UI v2
// 信息架构锚）。

// TasksPanel：Task 表 + 行内 runs 展开 + scale/stop/renew/delete。
export function TasksPanel({ projectId }: { projectId: string }) {
  return <TasksTab projectId={projectId} />;
}

// SchedulesPanel：时区 cron Schedule 表 + create/trigger/delete。
export function SchedulesPanel({ projectId }: { projectId: string }) {
  return <SchedulesTab projectId={projectId} />;
}

// kvLines 文本域 → 重复旗标面（K=V 行）。
function kvLines(text: string): Array<string> {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "" && !line.startsWith("#"));
}

// NewTaskButton 是 Tasks 页头主创建钮（对齐批 4：创建入口收口 PageHeader
// 右上，Apps 基准同款）——弹窗与 mutation 自包含。
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
      <Modal title="New task" open={createOpen} onClose={() => setCreateOpen(false)}>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            create.mutate(undefined, { onSuccess: () => setCreateOpen(false) });
          }}
        >
          <div className="grid grid-cols-2 gap-3">
            <Field label="Image">
              <TextInput value={form.image} onChange={(event) => setForm({ ...form, image: event.target.value })} placeholder="busybox:1.37" autoFocus />
            </Field>
            <Field label="Name" hint="optional">
              <TextInput value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} />
            </Field>
          </div>
          <div className="grid grid-cols-3 gap-3">
            <Field label="Form">
              <Select value={form.form} onChange={(event) => setForm({ ...form, form: event.target.value })}>
                <option value="one-shot">one-shot</option>
                <option value="resident">resident pool</option>
              </Select>
            </Field>
            {form.form === "resident" ? (
              <Field label="Concurrency">
                <TextInput value={form.concurrency} onChange={(event) => setForm({ ...form, concurrency: event.target.value })} inputMode="numeric" />
              </Field>
            ) : null}
            <Field label="TTL seconds" hint="lease lifetime">
              <TextInput value={form.ttl} onChange={(event) => setForm({ ...form, ttl: event.target.value })} inputMode="numeric" />
            </Field>
          </div>
          <Field label="Network group" hint="optional dedicated network group (cross-attach via taskGroup:<name>)">
            <TextInput value={form.networkGroup} onChange={(event) => setForm({ ...form, networkGroup: event.target.value })} placeholder="dispatcher" />
          </Field>
          <Field label="Command" hint="argv elements separated by spaces (e.g. sh -c &quot;migrate up&quot;)">
            <TextInput value={form.command} onChange={(event) => setForm({ ...form, command: event.target.value })} placeholder="sh -c migrate up" />
          </Field>
          <Field label="Env" hint="one KEY=VALUE per line">
            <TextArea rows={2} value={form.env} onChange={(event) => setForm({ ...form, env: event.target.value })} />
          </Field>
          <Field label="Secret refs" hint="comma-separated project secret names">
            <TextInput value={form.secretRefs} onChange={(event) => setForm({ ...form, secretRefs: event.target.value })} />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="CPU millis">
              <TextInput value={form.cpuMillis} onChange={(event) => setForm({ ...form, cpuMillis: event.target.value })} inputMode="numeric" />
            </Field>
            <Field label="Memory MB">
              <TextInput value={form.memoryMb} onChange={(event) => setForm({ ...form, memoryMb: event.target.value })} inputMode="numeric" />
            </Field>
          </div>
          <MutationBanner pending={create.isPending} error={create.isError ? create.error : null} success={null} />
          <div className="flex justify-end">
            <PrimaryButton disabled={create.isPending || form.image === ""}>Create</PrimaryButton>
          </div>
        </form>
      </Modal>
    </>
  );
}

function TasksTab({ projectId }: { projectId: string }) {
  const tasks = useTasks(projectId);
  const [query, setQuery] = useState("");
  const filtered = useListFilter(tasks.data ?? [], query, (task: { id?: string; name?: string; form?: string; state?: string; image?: string }) => [task.name ?? "", task.id ?? "", task.form ?? "", task.state ?? ""]);
  const { page, pageCount, pageRows, setPage } = useClientPage(filtered);
  return (
    <section className="flex flex-col gap-3">
      <div className="rounded-xl border bg-card">
        <div className="px-3 pt-3">
          <ListToolbar
            label="tasks"
            value={query}
            onChange={setQuery}
            placeholder="Filter tasks..."
            total={(tasks.data ?? []).length}
            shown={filtered.length}
            actions={<NewTaskButton projectId={projectId} />}
          />
        </div>
        {projectId === "" ? (
          <EmptyNote label="Enter a project id to list its tasks." />
        ) : tasks.isPending ? (
          <LoadingNote label="Loading tasks…" />
        ) : tasks.isError ? (
          <ErrorNote error={tasks.error} />
        ) : (tasks.data ?? []).length === 0 ? (
          <EmptyNote label="No tasks." />
        ) : (
          <>
            <TableWrap className="rounded-none border-0">
              <table className="w-full text-sm">
                <TableHead columns={["Task", "Form", "State", "Image", "", ""]} />
                <tbody>
                  {pageRows.map((task) => (
                    <TaskRow key={task.id} task={task} />
                  ))}
                </tbody>
              </table>
            </TableWrap>
            <ListPagination page={page} pageCount={pageCount} setPage={setPage} total={filtered.length} />
          </>
        )}
      </div>
      <CliEquivalent command={`fleetly tasks list --project ${projectId}`} />
    </section>
  );
}

function TaskRow({ task }: { task: { id?: string; name?: string; form?: string; state?: string; status?: string; image?: string; project_id?: string } }) {
  const [expanded, setExpanded] = useState(false);
  const runs = useRuns(expanded ? task.id ?? "" : "");
  const [scaleTo, setScaleTo] = useState("");
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
      <tr className="border-b border/60 hover:bg-muted/40">
        <td className="px-3 py-2">
          <div className="font-medium text-foreground">
            <a
              href={`/p/${encodeURIComponent(task.project_id ?? "")}/tasks/${encodeURIComponent(task.id ?? "")}`}
              className="hover:text-sky-300 hover:underline"
              onClick={(event) => event.stopPropagation()}
            >
              {task.name || shortId(task.id)}
            </a>
          </div>
          <div className="font-mono text-xs text-muted-foreground" title={task.id}>
            {shortId(task.id)}
          </div>
        </td>
        <td className="px-3 py-2 text-xs text-muted-foreground">{task.form}</td>
        <td className="px-3 py-2 text-xs text-muted-foreground">{task.state ?? task.status ?? "—"}</td>
        <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{task.image}</td>
        <td className="px-3 py-2">
          <div className="flex flex-wrap items-center justify-end gap-1">
            <RowButton onClick={() => setExpanded((prev) => !prev)}>{expanded ? "hide runs" : "runs"}</RowButton>
            {task.form === "resident" ? (
              <span className="flex items-center gap-1">
                <TextInput
                  value={scaleTo}
                  onChange={(event) => setScaleTo(event.target.value)}
                  placeholder="n"
                  className="w-14 rounded border border bg-muted px-1.5 py-0.5 text-xs"
                />
                <RowButton disabled={scale.isPending || scaleTo === ""} onClick={() => void scale.mutate()}>
                  scale
                </RowButton>
              </span>
            ) : null}
            <RowButton disabled={renew.isPending} onClick={() => void renew.mutate()} title="Renew the owner lease">
              renew
            </RowButton>
            <DangerRowButton confirm={`Stop task ${task.name ?? task.id}?`} disabled={stop.isPending} onClick={() => void stop.mutate()}>
              stop
            </DangerRowButton>
            <DangerRowButton confirm={`Delete task ${task.name ?? task.id}?`} disabled={del.isPending} onClick={() => void del.mutate()}>
              delete
            </DangerRowButton>
          </div>
          {scale.isError ? <ErrorNote error={scale.error} /> : null}
          {stop.isError ? <ErrorNote error={stop.error} /> : null}
        </td>
      </tr>
      {expanded ? (
        <tr className="border-b bg-muted/30">
          <td colSpan={6} className="px-3 py-2">
            {runs.isPending ? (
              <LoadingNote label="Loading runs…" />
            ) : runs.isError ? (
              <ErrorNote error={runs.error} />
            ) : (runs.data ?? []).length === 0 ? (
              <EmptyNote label="No runs." />
            ) : (
              <table className="w-full text-xs">
                <TableHead columns={["Run", "State", "Exit", "Started", ""]} />
                <tbody>
                  {(runs.data ?? []).map((run) => (
                    <RunRow key={run.id} run={run} />
                  ))}
                </tbody>
              </table>
            )}
          </td>
        </tr>
      ) : null}
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
  return (
    <tr className="border-b border/40">
      <td className="px-3 py-1.5 font-mono text-muted-foreground" title={run.id}>
        {shortId(run.id)}
      </td>
      <td className="px-3 py-1.5 text-muted-foreground">{run.state ?? run.status ?? "—"}</td>
      <td className="px-3 py-1.5 font-mono text-muted-foreground">{run.exit_code ?? "—"}</td>
      <td className="px-3 py-1.5 text-muted-foreground">{formatTime(run.started_at ?? run.created_at)}</td>
      <td className="px-3 py-1.5 text-right">
        <DangerRowButton confirm={`Stop run ${run.id}?`} disabled={stop.isPending} onClick={() => void stop.mutate()}>
          stop
        </DangerRowButton>
      </td>
    </tr>
  );
}

// NewScheduleButton 是 Tasks 页 Schedules tab 的页头主创建钮（对齐批 4，
// 随 active tab 切换挂 PageHeader 右上）——弹窗与 mutation 自包含。
export function NewScheduleButton({ projectId }: { projectId: string }) {
  const [createOpen, setCreateOpen] = useState(false);
  const [form, setForm] = useState({ name: "", cron: "0 3 * * *", timezone: "UTC", image: "", command: "", env: "", ttl: "3600" });
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
      <Modal title="New schedule" open={createOpen} onClose={() => setCreateOpen(false)}>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            create.mutate(undefined, { onSuccess: () => setCreateOpen(false) });
          }}
        >
          <div className="grid grid-cols-2 gap-3">
            <Field label="Cron" hint="5-field, in the schedule's timezone">
              <TextInput value={form.cron} onChange={(event) => setForm({ ...form, cron: event.target.value })} autoFocus />
            </Field>
            <Field label="Timezone" hint="IANA name">
              <TextInput value={form.timezone} onChange={(event) => setForm({ ...form, timezone: event.target.value })} />
            </Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Image">
              <TextInput value={form.image} onChange={(event) => setForm({ ...form, image: event.target.value })} />
            </Field>
            <Field label="Name">
              <TextInput value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} />
            </Field>
          </div>
          <Field label="Command" hint="argv elements separated by spaces">
            <TextInput value={form.command} onChange={(event) => setForm({ ...form, command: event.target.value })} />
          </Field>
          <Field label="Env" hint="one KEY=VALUE per line">
            <TextArea rows={2} value={form.env} onChange={(event) => setForm({ ...form, env: event.target.value })} />
          </Field>
          <Field label="TTL seconds">
            <TextInput value={form.ttl} onChange={(event) => setForm({ ...form, ttl: event.target.value })} inputMode="numeric" />
          </Field>
          <MutationBanner pending={create.isPending} error={create.isError ? create.error : null} success={null} />
          <div className="flex justify-end">
            <PrimaryButton disabled={create.isPending || form.image === "" || form.cron === ""}>Create</PrimaryButton>
          </div>
        </form>
      </Modal>
    </>
  );
}

function SchedulesTab({ projectId }: { projectId: string }) {
  const schedules = useSchedules(projectId);
  const [query, setQuery] = useState("");
  const filtered = useListFilter(schedules.data ?? [], query, (schedule: { name?: string; cron?: string; image?: string }) => [schedule.name ?? "", schedule.cron ?? "", schedule.image ?? ""]);
  const { page, pageCount, pageRows, setPage } = useClientPage(filtered);
  return (
    <section className="flex flex-col gap-3">
      <div className="rounded-xl border bg-card">
        <div className="px-3 pt-3">
          <ListToolbar
            label="schedules"
            value={query}
            onChange={setQuery}
            placeholder="Filter schedules..."
            total={(schedules.data ?? []).length}
            shown={filtered.length}
            actions={<NewScheduleButton projectId={projectId} />}
          />
        </div>
        {projectId === "" ? (
          <EmptyNote label="Enter a project id to list its schedules." />
        ) : schedules.isPending ? (
          <LoadingNote label="Loading schedules…" />
        ) : schedules.isError ? (
          <ErrorNote error={schedules.error} />
        ) : (schedules.data ?? []).length === 0 ? (
          <EmptyNote label="No schedules." />
        ) : (
          <>
            <TableWrap className="rounded-none border-0">
              <table className="w-full text-sm">
                <TableHead columns={["Schedule", "Cron", "Image", "Next", ""]} />
                <tbody>
                  {pageRows.map((schedule) => (
                    <ScheduleRow key={schedule.id} schedule={schedule} />
                  ))}
                </tbody>
              </table>
            </TableWrap>
            <ListPagination page={page} pageCount={pageCount} setPage={setPage} total={filtered.length} />
          </>
        )}
      </div>
      <CliEquivalent command={`fleetly schedules list --project ${projectId}`} />
    </section>
  );
}

function ScheduleRow({ schedule }: { schedule: { id?: string; name?: string; cron?: string; timezone?: string; image?: string; next_run_at?: string } }) {
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
  return (
    <tr className="border-b border/60 hover:bg-muted/40">
      <td className="px-3 py-2">
        <div className="font-medium text-foreground">{schedule.name || shortId(schedule.id)}</div>
        <div className="font-mono text-xs text-muted-foreground" title={schedule.id}>
          {shortId(schedule.id)}
        </div>
      </td>
      <td className="px-3 py-2 font-mono text-xs text-foreground">
        {schedule.cron}
        {schedule.timezone ? <span className="ml-1 text-muted-foreground">({schedule.timezone})</span> : null}
      </td>
      <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{schedule.image}</td>
      <td className="px-3 py-2 text-xs text-muted-foreground">{formatTime(schedule.next_run_at)}</td>
      <td className="px-3 py-2 text-right">
        <div className="flex items-center justify-end gap-1">
          <RowButton disabled={trigger.isPending} onClick={() => void trigger.mutate()} title="Run now (does not move the next tick)">
            trigger
          </RowButton>
          <DangerRowButton confirm={`Delete schedule ${schedule.name ?? schedule.id}?`} disabled={del.isPending} onClick={() => void del.mutate()}>
            delete
          </DangerRowButton>
        </div>
        {trigger.isError ? <ErrorNote error={trigger.error} /> : null}
      </td>
    </tr>
  );
}
