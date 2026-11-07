import { useState } from "react";
import { useCollection, useCommand } from "@lynxland/uikit-core";
import { toast } from "sonner";
import { PlusIcon, Trash2Icon } from "lucide-react";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, type StatusTone } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select as NativeSelect } from "@/components/kit/ui/native-select";
import { describeApiError } from "@/kit/errors";
import { toastOnSettled } from "@/kit/toast";
import {
  streamsLookup,
  streamsResource,
  topicsLookup,
  topicsResource,
  type StreamRow,
  type TopicRow,
} from "@/kit/subresources";

// PubSub 子资源 tab（B8，ADR-0096 决策 10 / design §4.7）：engine 决定面
// ——kafka 协议族（kafka/redpanda，dialect=kafka）→ Topics；nats → Streams。
// 声明行 + admin 工具容器收敛：行状态 pending/ready/error 由收敛段推进（表格
// 状态列如实显示）；创建对话框落行；删除 = wide 档确认（PreviewDelete 删除
// 披露行先行——Topic/Stream 删除即数据删除，不可恢复）。Update 面在 CLI/API
// （`fleetly pubsubs topics|streams update`——确认三形态），Console v1 只承载
// 建/列/删（ListPage 表单 + 创建对话框 + 删除确认）。

// subStatusTone：pending=info / ready=success / error=warning。
export function subStatusTone(status: string | undefined): StatusTone {
  switch (status) {
    case "ready":
      return "success";
    case "error":
      return "warning";
    case "pending":
      return "info";
    default:
      return "neutral";
  }
}

// subresourceKindOf：dialect 判 kafka 协议族 → topic；nats → stream（engine
// 名兜底——目录 dialect 字段缺席时 engine=nats 即 stream）。
export function subresourceKindOf(engine: string | undefined): "topic" | "stream" {
  return engine === "nats" ? "stream" : "topic";
}

export function PubSubSubResourcesTab({
  projectId,
  pubsubId,
  engine,
  routeId,
}: {
  projectId: string;
  pubsubId: string;
  engine: string | undefined;
  routeId: string;
}) {
  return subresourceKindOf(engine) === "stream" ? (
    <StreamsPanel projectId={projectId} pubsubId={pubsubId} routeId={routeId} />
  ) : (
    <TopicsPanel projectId={projectId} pubsubId={pubsubId} routeId={routeId} />
  );
}

// ---- Topics ----

function TopicsPanel({ projectId, pubsubId, routeId }: { projectId: string; pubsubId: string; routeId: string }) {
  const topics = useCollection(topicsResource, {
    from: routeId,
    search: topicsLookup,
    scope: { projectId, pubsubId },
    query: { refetchInterval: 10_000 },
  });
  const [createOpen, setCreateOpen] = useState(false);
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-xs text-muted">
          Declared topics converge through the admin tool container. Replication factor is fixed at 1 (single broker);
          partitions only increase.
        </p>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <PlusIcon data-icon-start-inline />
          New topic
        </Button>
      </div>
      <div className="overflow-hidden rounded-xl border">
        <table className="w-full text-sm">
          <thead className="border-b bg-panel-2/40 text-left text-2xs font-semibold tracking-wide text-muted uppercase">
            <tr>
              <th className="px-3 py-2">Topic</th>
              <th className="px-3 py-2">Partitions</th>
              <th className="px-3 py-2">Retention</th>
              <th className="px-3 py-2">Status</th>
              <th className="px-3 py-2">Created</th>
              <th className="px-3 py-2 text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {topics.status === "loading" ? (
              <EmptyRow cols={6} text="Loading topics…" />
            ) : topics.rows.length === 0 ? (
              <EmptyRow cols={6} text="No topics declared yet." />
            ) : (
              topics.rows.map((topic) => <TopicTableRow key={topic.id} topic={topic} projectId={projectId} pubsubId={pubsubId} />)
            )}
          </tbody>
        </table>
      </div>
      <CreateTopicDialog open={createOpen} onOpenChange={setCreateOpen} projectId={projectId} pubsubId={pubsubId} />
    </div>
  );
}

function TopicTableRow({ topic, projectId, pubsubId }: { topic: TopicRow; projectId: string; pubsubId: string }) {
  return (
    <tr className="border-b last:border-b-0">
      <td className="px-3 py-2 font-mono text-xs">{topic.name}</td>
      <td className="px-3 py-2 text-xs">{topic.partitions ?? 1}</td>
      <td className="px-3 py-2 text-xs text-muted">
        {topic.retention_ms && Number(topic.retention_ms) > 0 ? `${topic.retention_ms}ms` : "engine default"}
      </td>
      <td className="px-3 py-2">
        <StatusBadge tone={subStatusTone(topic.status)}>{topic.status ?? "unknown"}</StatusBadge>
        {topic.status_error ? <div className="mt-1 text-2xs text-warning">{topic.status_error}</div> : null}
      </td>
      <td className="px-3 py-2 text-xs text-muted">
        <RelativeTime value={topic.created_at} />
      </td>
      <td className="px-3 py-2 text-right">
        <TopicDeleteButton projectId={projectId} pubsubId={pubsubId} topic={topic} />
      </td>
    </tr>
  );
}

export function CreateTopicDialog({
  open,
  onOpenChange,
  projectId,
  pubsubId,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  pubsubId: string;
}) {
  const [name, setName] = useState("");
  const [partitions, setPartitions] = useState("1");
  const [retention, setRetention] = useState("");
  const create = useCommand(topicsResource.commands.create, {
    scope: { projectId, pubsubId },
    onSettled: toastOnSettled(() => toast("Topic declared")),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New topic</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            void create
              .mutateAsync({
                pubsubId,
                name,
                partitions: Number(partitions) || 1,
                retentionMs: retention ? Number(retention) : undefined,
              })
              .then(() => onOpenChange(false))
              .catch(() => undefined);
          }}
        >
          <Field id="topic-name" label="Name">
            <Input id="topic-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="orders" />
          </Field>
          <Field id="topic-partitions" label="Partitions (only increase later)">
            <Input id="topic-partitions" inputMode="numeric" value={partitions} onChange={(e) => setPartitions(e.target.value)} />
          </Field>
          <Field id="topic-retention" label="retention.ms (empty = engine default)">
            <Input id="topic-retention" inputMode="numeric" value={retention} onChange={(e) => setRetention(e.target.value)} />
          </Field>
          {create.failure ? <p className="text-xs text-destructive">{describeApiError(create.failure)}</p> : null}
          <div className="flex justify-end">
            <Button type="submit" size="sm" disabled={create.pending || name.trim() === ""}>
              {create.pending ? "Declaring…" : "Declare topic"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ---- Streams ----

function StreamsPanel({ projectId, pubsubId, routeId }: { projectId: string; pubsubId: string; routeId: string }) {
  const streams = useCollection(streamsResource, {
    from: routeId,
    search: streamsLookup,
    scope: { projectId, pubsubId },
    query: { refetchInterval: 10_000 },
  });
  const [createOpen, setCreateOpen] = useState(false);
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-xs text-muted">
          Declared JetStream streams converge through nats-box. Storage and retention are immutable after create.
        </p>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <PlusIcon data-icon-start-inline />
          New stream
        </Button>
      </div>
      <div className="overflow-hidden rounded-xl border">
        <table className="w-full text-sm">
          <thead className="border-b bg-panel-2/40 text-left text-2xs font-semibold tracking-wide text-muted uppercase">
            <tr>
              <th className="px-3 py-2">Stream</th>
              <th className="px-3 py-2">Subjects</th>
              <th className="px-3 py-2">Retention · Storage</th>
              <th className="px-3 py-2">Status</th>
              <th className="px-3 py-2">Created</th>
              <th className="px-3 py-2 text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {streams.status === "loading" ? (
              <EmptyRow cols={6} text="Loading streams…" />
            ) : streams.rows.length === 0 ? (
              <EmptyRow cols={6} text="No streams declared yet." />
            ) : (
              streams.rows.map((stream) => (
                <StreamTableRow key={stream.id} stream={stream} projectId={projectId} pubsubId={pubsubId} />
              ))
            )}
          </tbody>
        </table>
      </div>
      <CreateStreamDialog open={createOpen} onOpenChange={setCreateOpen} projectId={projectId} pubsubId={pubsubId} />
    </div>
  );
}

function StreamTableRow({ stream, projectId, pubsubId }: { stream: StreamRow; projectId: string; pubsubId: string }) {
  return (
    <tr className="border-b last:border-b-0">
      <td className="px-3 py-2 font-mono text-xs">{stream.name}</td>
      <td className="px-3 py-2 font-mono text-xs text-muted">{(stream.subjects ?? []).join(", ") || "—"}</td>
      <td className="px-3 py-2 text-xs">
        {stream.retention} · {stream.storage}
      </td>
      <td className="px-3 py-2">
        <StatusBadge tone={subStatusTone(stream.status)}>{stream.status ?? "unknown"}</StatusBadge>
        {stream.status_error ? <div className="mt-1 text-2xs text-warning">{stream.status_error}</div> : null}
      </td>
      <td className="px-3 py-2 text-xs text-muted">
        <RelativeTime value={stream.created_at} />
      </td>
      <td className="px-3 py-2 text-right">
        <StreamDeleteButton projectId={projectId} pubsubId={pubsubId} stream={stream} />
      </td>
    </tr>
  );
}

function CreateStreamDialog({
  open,
  onOpenChange,
  projectId,
  pubsubId,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  pubsubId: string;
}) {
  const [name, setName] = useState("");
  const [subjects, setSubjects] = useState("");
  const [retention, setRetention] = useState("limits");
  const [storage, setStorage] = useState("file");
  const create = useCommand(streamsResource.commands.create, {
    scope: { projectId, pubsubId },
    onSettled: toastOnSettled(() => toast("Stream declared")),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New stream</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            void create
              .mutateAsync({ pubsubId, name, subjects, retention, storage })
              .then(() => onOpenChange(false))
              .catch(() => undefined);
          }}
        >
          <Field id="stream-name" label="Name">
            <Input id="stream-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="ORDERS" />
          </Field>
          <Field id="stream-subjects" label="Subjects (comma separated)">
            <Input id="stream-subjects" value={subjects} onChange={(e) => setSubjects(e.target.value)} placeholder="orders.>" />
          </Field>
          <Field id="stream-retention" label="Retention (immutable after create)">
            <NativeSelect id="stream-retention" value={retention} onChange={(e) => setRetention(e.target.value)}>
              <option value="limits">limits</option>
              <option value="interest">interest</option>
              <option value="workqueue">workqueue</option>
            </NativeSelect>
          </Field>
          <Field id="stream-storage" label="Storage (immutable; memory streams cannot be backed up)">
            <NativeSelect id="stream-storage" value={storage} onChange={(e) => setStorage(e.target.value)}>
              <option value="file">file</option>
              <option value="memory">memory</option>
            </NativeSelect>
          </Field>
          {create.failure ? <p className="text-xs text-destructive">{describeApiError(create.failure)}</p> : null}
          <div className="flex justify-end">
            <Button type="submit" size="sm" disabled={create.pending || name.trim() === ""}>
              {create.pending ? "Declaring…" : "Declare stream"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ---- 共享件：删除（wide 档：PreviewDelete 披露行 → 确认 → 带票删） ----

// TopicDeleteButton / StreamDeleteButton：两段流——①预演铸票（删除披露行
// = 服务端 disclosure，live 数据量可得则列）→ ②确认框（wide 档 Consequences
// 先行）→ 带票删除。预演失败直接 toast（披露不可得不得静默删除）。

export function TopicDeleteButton({ projectId, pubsubId, topic }: { projectId: string; pubsubId: string; topic: TopicRow }) {
  const [open, setOpen] = useState(false);
  const [disclosure, setDisclosure] = useState("");
  const [ticket, setTicket] = useState("");
  const preview = useCommand(topicsResource.commands.previewDelete, {
    scope: { projectId, pubsubId },
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        setDisclosure(result.payload.disclosure ?? "");
        setTicket(result.payload.ticket ?? "");
        setOpen(true);
        return;
      }
      if (failure != null) toast.error(describeApiError(failure));
    },
  });
  const remove = useCommand(topicsResource.commands.delete, {
    scope: { projectId, pubsubId },
    onSettled: toastOnSettled(() => toast("Topic deleted — the convergence segment removes the live topic")),
  });
  const id = topic.id ?? "";
  return (
    <>
      <Button
        size="sm"
        variant="ghost"
        aria-label={`Delete topic ${topic.name ?? ""}`}
        disabled={preview.pending}
        onClick={() => preview.mutate({ pubsubId, id })}
      >
        <Trash2Icon />
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={`Delete topic ${topic.name ?? ""}?`}
        description="Deleting a topic destroys its partitioned data."
        confirmLabel="Delete topic"
        pendingLabel="Deleting…"
        risk={{ tier: "wide", consequences: <p className="text-xs">{disclosure}</p> }}
        onConfirm={() => remove.mutateAsync({ pubsubId, id, ticket })}
      />
    </>
  );
}

export function StreamDeleteButton({ projectId, pubsubId, stream }: { projectId: string; pubsubId: string; stream: StreamRow }) {
  const [open, setOpen] = useState(false);
  const [disclosure, setDisclosure] = useState("");
  const [ticket, setTicket] = useState("");
  const preview = useCommand(streamsResource.commands.previewDelete, {
    scope: { projectId, pubsubId },
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        setDisclosure(result.payload.disclosure ?? "");
        setTicket(result.payload.ticket ?? "");
        setOpen(true);
        return;
      }
      if (failure != null) toast.error(describeApiError(failure));
    },
  });
  const remove = useCommand(streamsResource.commands.delete, {
    scope: { projectId, pubsubId },
    onSettled: toastOnSettled(() => toast("Stream deleted — the convergence segment removes the live stream")),
  });
  const id = stream.id ?? "";
  return (
    <>
      <Button
        size="sm"
        variant="ghost"
        aria-label={`Delete stream ${stream.name ?? ""}`}
        disabled={preview.pending}
        onClick={() => preview.mutate({ pubsubId, id })}
      >
        <Trash2Icon />
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={`Delete stream ${stream.name ?? ""}?`}
        description="Deleting a stream destroys its retained messages."
        confirmLabel="Delete stream"
        pendingLabel="Deleting…"
        risk={{ tier: "wide", consequences: <p className="text-xs">{disclosure}</p> }}
        onConfirm={() => remove.mutateAsync({ pubsubId, id, ticket })}
      />
    </>
  );
}

function EmptyRow({ cols, text }: { cols: number; text: string }) {
  return (
    <tr>
      <td colSpan={cols} className="px-3 py-6 text-center text-xs text-muted">
        {text}
      </td>
    </tr>
  );
}

function Field({ id, label, children }: { id: string; label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
    </div>
  );
}
