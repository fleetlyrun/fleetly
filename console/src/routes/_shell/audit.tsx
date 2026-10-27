import { createFileRoute } from "@tanstack/react-router";
import { useCollection } from "@lynx-go/uikit-core";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { formatAbsolute } from "@/lib/format";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage as FleetListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { auditResource, auditSearch, type AuditRow } from "@/kit/audit";

// 审计页（F3.1 → UI v2 批 5 reskin → 2026-10-26 批1 useCollection 化）：
// GET /v1/audit 只读面（整取 limit 100 + 客户端求值方言）。旧页的提交式
// 服务器过滤（source 下拉/action 前缀/actor/resource 表单）退役，改为
// FilterBar 即时过滤（q 覆盖 source/actor/action/resource，进 URL）——
// 行为增量：即时生效 + 可分享。审计行服务端只写——Console 是观察面。
export const Route = createFileRoute("/_shell/audit")({
  validateSearch: auditSearch.toSearchSchema(),
  component: AuditPage,
});

const columns: LegacyColumnDef<AuditRow, any>[] = [
  {
    accessorKey: "created_at",
    header: "Time",
    meta: { sortField: "created_at" },
    cell: ({ row }) => (
      <span className="whitespace-nowrap text-xs text-muted" title={formatAbsolute(row.original.created_at)}>
        {formatAbsolute(row.original.created_at)}
      </span>
    ),
  },
  {
    accessorKey: "source",
    header: "Source",
    meta: { sortField: "source" },
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
    meta: { sortField: "action" },
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
    // FleetListPage 自带页面衬垫与 header 槽（收尾批统一 Layout：去掉
    // PageFrame 外壳——双衬垫 + 双层宽度容器随统一列退役）。
    <FleetListPage
      list={list}
      title="Audit"
      description="Server-written audit records (GET /v1/audit) — the console observes, never writes"
      toolbar={<FilterBar list={list} schema={auditSearch} textPlaceholder="Filter audit..." />}
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No audit entries match the filter" : "No audit entries"}
      emptyDescription={
        filtered
          ? "Adjust or clear the filter to see rows again."
          : "Act on the platform to populate the audit view."
      }
      appendix={<CliEquivalent command="fleetly audit --limit 100" />}
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
    </FleetListPage>
  );
}
