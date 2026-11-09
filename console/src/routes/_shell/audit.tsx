import { createFileRoute } from "@tanstack/react-router";
import { useCollection } from "@lynxland/uikit-core";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { formatAbsolute } from "@/lib/format";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage } from "@/components/kit/query/list-page";
import { KeysetPagination } from "@/components/kit/query/keyset-pagination";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { PageFrame } from "@/components/domain/page-frame";
import { auditResource, auditSearch, type AuditRow } from "@/kit/audit";

// 审计页（F3.1 → UI v2 批 5 reskin → 2026-10-26 批1 useCollection 化 →
// 2026-11-10 服务端游标翻页批）：GET /v1/audit 只读面，before_seq keyset
// 方言（URL cursor → before_seq，页大小 100，KeysetPagination 承载翻页）。
// FilterBar 即时过滤（q 覆盖 source/actor/action/resource，进 URL）保留，
// 语义为页内快筛（服务端无通用 q；精确过滤走 proto 字段）。列头排序随
// keyset 方言退役（服务端序固定 seq DESC；客户端排序只排当前页会撒谎）。
// 审计行服务端只写——Console 是观察面。
export const Route = createFileRoute("/_shell/audit")({
  validateSearch: auditSearch.toSearchSchema(),
  component: AuditPage,
});

const columns: LegacyColumnDef<AuditRow, any>[] = [
  {
    accessorKey: "created_at",
    header: "Time",
    cell: ({ row }) => (
      <span className="whitespace-nowrap text-xs text-muted" title={formatAbsolute(row.original.created_at)}>
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
      <span className="font-mono text-xs text-muted" title={row.original.actor}>
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
      <span className="font-mono text-xs text-muted" title={row.original.resource}>
        {row.original.resource}
      </span>
    ),
  },
  {
    id: "fingerprints",
    header: "Before → After",
    cell: ({ row }) => (
      <span className="font-mono text-2xs text-muted opacity-80">
        {row.original.before_fp ? <span title={row.original.before_fp}>{row.original.before_fp.slice(0, 8)}…</span> : "∅"} →{" "}
        {row.original.after_fp ? <span title={row.original.after_fp}>{row.original.after_fp.slice(0, 8)}…</span> : "∅"}
      </span>
    ),
  },
];

function AuditPage() {
  const list = useCollection(auditResource, {
    from: Route.id,
    search: auditSearch,
    query: { refetchInterval: 15_000 },
  });

  // 源空与过滤空两套文案：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    // 一页一壳（2026-10-28 机制收口）：壳 = PageFrame（衬垫 + 页头三件
    // 单源）；kit ListPage 降为壳内模块配方（kit 件零页面 chrome）。
    <PageFrame
      title="Audit"
      description="Server-written audit records (GET /v1/audit) — the console observes, never writes"
    >
      <ListPage
        list={list}
        toolbar={<FilterBar list={list} schema={auditSearch} textPlaceholder="Filter audit..." />}
        pagination={<KeysetPagination list={list} />}
        emptyTitle={filtered ? "No audit entries match the filter" : "No audit entries"}
        emptyDescription={
          filtered
            ? "Adjust or clear the filter to see rows again."
            : "Act on the platform to populate the audit view."
        }
        appendix={<CliEquivalent command="fleetly audit --limit 100" />}
      >
        <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
      </ListPage>
    </PageFrame>
  );
}
