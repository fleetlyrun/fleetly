import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { formatAbsolute } from "@/lib/format";
import { useAudit } from "@/lib/catalog";
import { EmptyState } from "@/components/domain/empty-state";
import { ErrorState } from "@/components/domain/error-state";
import { PageHeader } from "@/components/domain/page-header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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

// 审计页（F3.1 → UI v2 批 5 reskin）：GET /v1/audit 只读面 + CLI 同款
// 过滤器（source/action 前缀/actor/resource）。审计行服务端只写——
// Console 是观察面。
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

  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader title="Audit" description="Server-written audit trail (GET /v1/audit) — the console observes, never writes" />
      <form
        className="mb-4 flex flex-wrap items-end gap-2.5"
        onSubmit={(event) => {
          event.preventDefault();
          setActive({ source, action, actor, resource });
        }}
      >
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Source</Label>
          <Select value={source} onValueChange={setSource}>
            <SelectTrigger className="w-32">
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
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Action prefix</Label>
          <Input value={action} onChange={(event) => setAction(event.target.value)} placeholder="deployment." className="h-8 w-36 font-mono text-xs" />
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Actor</Label>
          <Input value={actor} onChange={(event) => setActor(event.target.value)} className="h-8 w-32" />
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Resource</Label>
          <Input value={resource} onChange={(event) => setResource(event.target.value)} className="h-8 w-40" />
        </div>
        <Button type="submit" size="sm" className="mb-0.5">
          Filter
        </Button>
      </form>

      <div className="rounded-xl border bg-card">
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
            ) : (audit.data ?? []).length === 0 ? (
              <TableRow>
                <TableCell colSpan={6}>
                  <EmptyState icon={AuditPlaceholderIcon} title="No audit entries match" description="Widen the filters, or act on the platform to populate the trail." />
                </TableCell>
              </TableRow>
            ) : (
              (audit.data ?? []).map((entry) => (
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
      </div>
    </div>
  );
}

import { ScrollTextIcon as AuditPlaceholderIcon } from "lucide-react";
