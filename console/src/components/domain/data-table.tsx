import { useState, type ReactNode } from "react";
import { flexRender } from "@tanstack/react-table";
import {
  getCoreRowModel,
  getSortedRowModel,
  useLegacyTable,
  type LegacyColumnDef,
  type LegacyReactTable,
} from "@tanstack/react-table/legacy";
import { ArrowDownIcon, ArrowUpIcon, ChevronsUpDownIcon } from "lucide-react";
import { cn } from "cn";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

// DataTable（List 原型解剖，UI v2）：TanStack Table 底座的统一资源表。
// 四态在此收口——loading=骨架行严格占位最终布局 / empty=页面给 EmptyState
// 槽 / error=ErrorInline 带重试 / data=可排序行（行操作由列自带）。工具条
// （搜索/筛选）属页面层，不进本件。
// 底座注记：@tanstack/react-table 9.x 原生是 store/atom 新范式，legacy
// 子入口是官方 v8 形态兼容层——单源文件隔离未来迁移成本。
type SortingState = ReturnType<LegacyReactTable<never>["getState"]>["sorting"];

export function DataTable<T extends Record<string, any>>({
  data,
  columns,
  loading = false,
  error = null,
  onRetry,
  empty = null,
  onRowClick,
  skeletonRows = 5,
  className,
}: {
  data: T[] | undefined;
  columns: LegacyColumnDef<T, any>[];
  loading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  empty?: ReactNode;
  onRowClick?: (row: T) => void;
  skeletonRows?: number;
  className?: string;
}) {
  const [sorting, setSorting] = useState<SortingState>([]);
  const table = useLegacyTable<T>({
    data: data ?? [],
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  });

  if (error != null) {
    return (
      <div className="px-2 py-6">
        <ErrorInline error={error} onRetry={onRetry} />
      </div>
    );
  }

  const rows = table.getRowModel().rows;
  const columnCount = columns.length;
  return (
    <Table className={className}>
      <TableHeader>
        {table.getHeaderGroups().map((headerGroup) => (
          <TableRow key={headerGroup.id} className="hover:bg-transparent">
            {headerGroup.headers.map((header) => {
              const canSort = header.column.getCanSort();
              const sorted = header.column.getIsSorted();
              return (
                <TableHead key={header.id}>
                  {canSort ? (
                    <button
                      type="button"
                      onClick={header.column.getToggleSortingHandler()}
                      className="-mx-1 inline-flex items-center gap-1 rounded-sm px-1 hover:text-foreground"
                    >
                      {flexRender(header.column.columnDef.header, header.getContext())}
                      {sorted === "asc" ? (
                        <ArrowUpIcon className="size-3" />
                      ) : sorted === "desc" ? (
                        <ArrowDownIcon className="size-3" />
                      ) : (
                        <ChevronsUpDownIcon className="size-3 opacity-50" />
                      )}
                    </button>
                  ) : (
                    flexRender(header.column.columnDef.header, header.getContext())
                  )}
                </TableHead>
              );
            })}
          </TableRow>
        ))}
      </TableHeader>
      <TableBody>
        {loading ? (
          Array.from({ length: skeletonRows }).map((_, rowIndex) => (
            <TableRow key={`sk-${rowIndex}`}>
              {columns.map((_, colIndex) => (
                <TableCell key={colIndex}>
                  <Skeleton className="h-5 w-full max-w-40" />
                </TableCell>
              ))}
            </TableRow>
          ))
        ) : rows.length === 0 ? (
          <TableRow className="hover:bg-transparent">
            <TableCell colSpan={columnCount} className="p-0">
              {empty}
            </TableCell>
          </TableRow>
        ) : (
          rows.map((row) => (
            <TableRow
              key={row.id}
              data-state={row.getIsSelected() ? "selected" : undefined}
              className={cn(onRowClick && "cursor-pointer")}
              onClick={onRowClick ? () => onRowClick(row.original) : undefined}
            >
              {row.getVisibleCells().map((cell) => (
                <TableCell key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</TableCell>
              ))}
            </TableRow>
          ))
        )}
      </TableBody>
    </Table>
  );
}

// ErrorInline：表格体内的错误行（批 1 接 describeError 分状态文案升级）。
function ErrorInline({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const detail =
    error instanceof ApiError ? `${error.code}: ${error.message}` : error instanceof Error ? error.message : String(error);
  return (
    <div className="flex flex-col items-start gap-2 rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-4 py-3">
      <div className="text-sm font-semibold text-[var(--status-danger)]">Request failed</div>
      <div className="font-mono text-xs break-all text-[var(--status-danger)]/80">{detail}</div>
      {onRetry ? (
        <Button variant="outline" size="sm" onClick={onRetry}>
          Retry
        </Button>
      ) : null}
    </div>
  );
}
