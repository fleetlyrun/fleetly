import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { formatAbsolute } from "@/lib/format";
import { useAudit } from "@/lib/catalog";
import { EmptyState } from "@/components/domain/empty-state";
import { ErrorState } from "@/components/domain/error-state";
import { CliEquivalent, ListPagination, ListToolbar, useClientPage, useListFilter } from "@/components/domain/list-toolbar";
import { PageHeader } from "@/components/domain/page-header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

// 审计页（F3.1 → UI v2 批 5 reskin；对齐批 5 收编 Apps 列表规范）：GET
// /v1/audit 只读面 + CLI 同款服务器过滤器（source/action 前缀/actor/
// resource，Filter 提交生效）在工具栏 children 槽，主输入为页内快筛。
// 审计行服务端只写——Console 是观察面。
const SOURCES = ["", "api", "cli", "manual", "webhook", "schedule", "system"];

export const Route = createFileRoute("/_shell/audit")({
  component: AuditPageV2,
});

function AuditPageV2() {
  const [source, setSource] = useState("");
  const [action, setAction] = useState("");
  const [actor, setActor] = useState("");
  const [resource, setResource] = useState("");
  const [active, setActive] = useState({ source: "", action: "", actor: "", resource: "" });
  const audit = useAudit({ ...active, limit: 100 });
  const entries = audit.data ?? [];
  const [query, setQuery] = useState("");
  const filtered = useListFilter(entries, query, (entry: { source?: string; actor?: string; action?: string; resource?: string }) => [
    entry.source ?? "",
    entry.actor ?? "",
    entry.action ?? "",
    entry.resource ?? "",
  ]);
  const { page, pageCount, pageRows, setPage } = useClientPage(filtered);

  return (
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader title="Audit" description="Server-written audit trail (GET /v1/audit) — the console observes, never writes" />
      <div className="rounded-xl border bg-card">
        <form
          onSubmit={(event) => {
            event.preventDefault();
            setActive({ source, action, actor, resource });
          }}
        >
          <div className="px-3 pt-3">
            <ListToolbar
              label="audit"
              value={query}
              onChange={setQuery}
              placeholder="Filter audit..."
              total={entries.length}
              shown={filtered.length}
              actions={
                <Button type="submit" size="sm">
                  Filter
                </Button>
              }
            >
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
              <Input
                value={action}
                onChange={(event) => setAction(event.target.value)}
                placeholder="action prefix"
                aria-label="Action prefix"
                className="h-8 w-32 font-mono text-xs"
              />
              <Input
                value={actor}
                onChange={(event) => setActor(event.target.value)}
                placeholder="actor"
                aria-label="Actor"
                className="h-8 w-28"
              />
              <Input
                value={resource}
                onChange={(event) => setResource(event.target.value)}
                placeholder="resource"
                aria-label="Resource"
                className="h-8 w-36"
              />
            </ListToolbar>
          </div>
        </form>
        <Table className="text-[13px]">
          <TableHeader>
            <TableRow>
              <TableHead>Time</TableHead>
              <TableHead>Source</TableHead>
              <TableHead>Actor</TableHead>
              <TableHead>Action</TableHead>
              <TableHead>Resource</TableHead>
              <TableHead>Before → After</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {audit.isPending ? (
              Array.from({ length: 6 }).map((_, index) => (
                <TableRow key={index}>
                  {Array.from({ length: 6 }).map((_, cell) => (
                    <TableCell key={cell}>
                      <Skeleton className="h-4 w-20" />
                    </TableCell>
                  ))}
                </TableRow>
              ))
            ) : audit.isError ? (
              <TableRow>
                <TableCell colSpan={6}>
                  <ErrorState error={audit.error} onRetry={() => void audit.refetch()} />
                </TableCell>
              </TableRow>
            ) : entries.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6}>
                  <EmptyState icon={AuditPlaceholderIcon} title="No audit entries match" description="Widen the filters, or act on the platform to populate the trail." />
                </TableCell>
              </TableRow>
            ) : filtered.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6} className="py-8 text-center text-sm text-muted-foreground">
                  No entries match the filter.
                </TableCell>
              </TableRow>
            ) : (
              pageRows.map((entry) => (
                <TableRow key={`${entry.created_at}-${entry.action}-${entry.resource}`} className="align-top">
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground" title={formatAbsolute(entry.created_at)}>
                    {formatAbsolute(entry.created_at)}
                  </TableCell>
                  <TableCell className="text-xs">{entry.source}</TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground" title={entry.actor}>
                    {entry.actor}
                  </TableCell>
                  <TableCell className="font-mono text-xs">{entry.action}</TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground" title={entry.resource}>
                    {entry.resource}
                  </TableCell>
                  <TableCell className="font-mono text-[11px] text-muted-foreground opacity-80">
                    {entry.before_fp ? <span title={entry.before_fp}>{entry.before_fp.slice(0, 8)}…</span> : "∅"} →{" "}
                    {entry.after_fp ? <span title={entry.after_fp}>{entry.after_fp.slice(0, 8)}…</span> : "∅"}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
        <ListPagination page={page} pageCount={pageCount} setPage={setPage} total={filtered.length} />
      </div>
      <CliEquivalent command="fleetly audit --limit 100" />
    </div>
  );
}

import { ScrollTextIcon as AuditPlaceholderIcon } from "lucide-react";
