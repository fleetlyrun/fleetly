import { useState, type ReactNode } from "react";
import { SearchXIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { DataTable } from "./data-table";
import { EmptyState } from "./empty-state";
import { CliEquivalent, ListToolbar, useListFilter } from "./list-toolbar";
import { PageFrame } from "./page-frame";

// 简单 CRUD 列表模板（List 原型整页 = ListPage；卡体 = ListCard）。
// 工具栏（快筛 + 过滤 slot + 计数 + 主动作槽）+ 表格四态（骨架/错误/
// 源空/过滤空）+ 客户端分页 + CLI equivalent 行在模板内收口——页面只
// 声明列、过滤字段、空态文案与主动作，不再手装卡壳与状态行。对齐批 5
// 裁决随模板固化：列表主动作钉工具栏行最右，页头钮位退役（ListPage 不
// 设页头 actions 槽）。
//
// 逃生舱：children 槽替换表格体（保留工具栏/卡壳/CLI 行），供行级动作
// 错误行等特殊表格使用——特殊不等于回到手装全页。
export interface ListCardProps<T extends Record<string, any>> {
  label: string;
  placeholder: string;
  rows: T[];
  fieldsOf: (row: T) => string[];
  loading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  columns?: LegacyColumnDef<T, any>[];
  onRowClick?: (row: T) => void;
  pageSize?: number;
  skeletonRows?: number;
  filters?: ReactNode;
  actions?: ReactNode;
  onFilterSubmit?: () => void;
  empty?: ReactNode;
  filterEmpty?: ReactNode;
  cli?: string;
  className?: string;
  children?: ReactNode | ((context: { rows: T[]; query: string }) => ReactNode);
}

// ListCard 是列表卡体：rounded-xl 卡壳 + 工具栏 + 表格四态 + 分页 +
// （可选）CLI 行。一页多模块页的 tab 模块直接复用本件。
export function ListCard<T extends Record<string, any>>({
  label,
  placeholder,
  rows,
  fieldsOf,
  loading,
  error,
  onRetry,
  columns,
  onRowClick,
  pageSize,
  skeletonRows,
  filters,
  actions,
  onFilterSubmit,
  empty,
  filterEmpty,
  cli,
  className,
  children,
}: ListCardProps<T>) {
  const [query, setQuery] = useState("");
  const filtered = useListFilter(rows, query, fieldsOf);
  const toolbar = (
    <ListToolbar
      label={label}
      value={query}
      onChange={setQuery}
      placeholder={placeholder}
      total={rows.length}
      shown={filtered.length}
      actions={actions}
    >
      {filters}
    </ListToolbar>
  );
  const emptyNode =
    filtered.length === 0
      ? query.trim() !== ""
        ? (filterEmpty ?? (
            <EmptyState
              icon={SearchXIcon}
              title={`No ${label} match the filter`}
              description="Adjust or clear the filter to see rows again."
            />
          ))
        : empty
      : undefined;
  const body =
    typeof children === "function" ? (
      children({ rows: filtered, query })
    ) : children ? (
      children
    ) : columns ? (
      <DataTable
        data={filtered}
        columns={columns}
        loading={loading}
        error={error}
        onRetry={onRetry}
        empty={emptyNode}
        onRowClick={onRowClick}
        pageSize={pageSize}
        skeletonRows={skeletonRows}
        className={className}
      />
    ) : null;
  return (
    <>
      <div className="rounded-xl border bg-card">
        {onFilterSubmit ? (
          <form
            onSubmit={(event) => {
              event.preventDefault();
              onFilterSubmit();
            }}
          >
            <div className="px-3 pt-3">{toolbar}</div>
          </form>
        ) : (
          <div className="px-3 pt-3">{toolbar}</div>
        )}
        {body}
      </div>
      {cli ? <CliEquivalent command={cli} /> : null}
    </>
  );
}

// ListPage 是 List 原型整页模板：PageFrame（壳 + 页头）+ ListCard。
// appendix 槽承接列表卡之后的页面附加物（揭示弹窗、脚注等）——弹窗不进
// 卡体，壳不由页面手拼。
export function ListPage<T extends Record<string, any>>({
  width,
  breadcrumb,
  title,
  description,
  appendix,
  ...card
}: ListCardProps<T> & {
  width?: "wide" | "narrow";
  breadcrumb?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  appendix?: ReactNode;
}) {
  return (
    <PageFrame width={width} breadcrumb={breadcrumb} title={title} description={description}>
      <ListCard {...card} />
      {appendix}
    </PageFrame>
  );
}
