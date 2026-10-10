import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { ScrollTextIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { formatAbsolute } from "@/lib/format";
import { useAudit } from "@/lib/catalog";
import { EmptyState } from "@/components/domain/empty-state";
import { ListPage } from "@/components/domain/list-page";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

// 审计页（F3.1 → UI v2 批 5 reskin；模板化收编 ListPage）：GET /v1/audit
// 只读面 + CLI 同款服务器过滤器（source/action 前缀/actor/resource，
// Filter 提交生效）走 ListPage 的 onFilterSubmit（form 包工具栏）+
// filters 槽，快筛为页内过滤。审计行服务端只写——Console 是观察面。
const SOURCES = ["", "api", "cli", "manual", "webhook", "schedule", "system"];

type AuditEntry = NonNullable<ReturnType<typeof useAudit>["data"]>[number];

export const Route = createFileRoute("/_shell/audit")({
  component: AuditPage,
});

const columns: LegacyColumnDef<AuditEntry, any>[] = [
  {
    accessorKey: "created_at",
    header: "Time",
    cell: ({ row }) => (
      <span className="whitespace-nowrap text-xs text-muted-foreground" title={formatAbsolute(row.original.created_at)}>
        {formatAbsolute(row.original.created_at)}
      </span>
    ),
  },
  {
    accessorKey: "source",
    header: "Source",
    cell: ({ row }) => <span className="text-xs">{row.original.source}</span>,
  },
  {
    accessorKey: "actor",
    header: "Actor",
    cell: ({ row }) => (
      <span className="font-mono text-xs text-muted-foreground" title={row.original.actor}>
        {row.original.actor}
      </span>
    ),
  },
  {
    accessorKey: "action",
    header: "Action",
    cell: ({ row }) => <span className="font-mono text-xs">{row.original.action}</span>,
  },
  {
    accessorKey: "resource",
    header: "Resource",
    cell: ({ row }) => (
      <span className="font-mono text-xs text-muted-foreground" title={row.original.resource}>
        {row.original.resource}
      </span>
    ),
  },
  {
    id: "fingerprints",
    header: "Before → After",
    cell: ({ row }) => (
      <span className="font-mono text-[11px] text-muted-foreground opacity-80">
        {row.original.before_fp ? <span title={row.original.before_fp}>{row.original.before_fp.slice(0, 8)}…</span> : "∅"} →{" "}
        {row.original.after_fp ? <span title={row.original.after_fp}>{row.original.after_fp.slice(0, 8)}…</span> : "∅"}
      </span>
    ),
  },
];

function AuditPage() {
  const [source, setSource] = useState("");
  const [action, setAction] = useState("");
  const [actor, setActor] = useState("");
  const [resource, setResource] = useState("");
  const [active, setActive] = useState({ source: "", action: "", actor: "", resource: "" });
  const audit = useAudit({ ...active, limit: 100 });
  const entries = audit.data ?? [];

  return (
    <ListPage
      title="Audit"
      description="Server-written audit trail (GET /v1/audit) — the console observes, never writes"
      label="audit entries"
      placeholder="Filter audit..."
      rows={entries}
      fieldsOf={(entry) => [entry.source ?? "", entry.actor ?? "", entry.action ?? "", entry.resource ?? ""]}
      loading={audit.isPending}
      error={audit.isError ? audit.error : null}
      onRetry={() => void audit.refetch()}
      columns={columns}
      skeletonRows={6}
      filters={
        <>
          <Select value={source} onValueChange={setSource}>
            <SelectTrigger aria-label="Source" className="h-8 w-28">
              <SelectValue>{source === "" ? "any" : source}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {SOURCES.map((item) => (
                <SelectItem key={item || "any"} value={item}>
                  {item === "" ? "any" : item}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input value={action} onChange={(event) => setAction(event.target.value)} placeholder="action prefix" aria-label="Action prefix" className="h-8 w-32 font-mono text-xs" />
          <Input value={actor} onChange={(event) => setActor(event.target.value)} placeholder="actor" aria-label="Actor" className="h-8 w-28" />
          <Input value={resource} onChange={(event) => setResource(event.target.value)} placeholder="resource" aria-label="Resource" className="h-8 w-36" />
        </>
      }
      actions={
        <Button type="submit" size="sm">
          Filter
        </Button>
      }
      onFilterSubmit={() => setActive({ source, action, actor, resource })}
      empty={
        <EmptyState
          icon={ScrollTextIcon}
          title="No audit entries match"
          description="Widen the filters, or act on the platform to populate the trail."
        />
      }
      cli="fleetly audit --limit 100"
    />
  );
}
