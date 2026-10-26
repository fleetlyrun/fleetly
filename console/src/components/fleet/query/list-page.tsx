// vendored from fleet-ui registry/query/list-page.tsx @0.1.0（2026-10-24 试点切片）
// 拷贝适配（design.md §12.1）：① token 方言类名按映射表改写；② 剥除上游
// 自带页壳（mx-auto max-w-5xl p-6）与块内 header——本仓壳与页头双双单源
// （PageFrame/PageHeader，守卫规则⑥立法意图 + 页头唯一形态法），页面经
// PageFrame 套本块，块只管卡体 × toolbar × 四态 × appendix。
import type { CollectionHandle } from "@lynx-go/uikit-core/react";
import type { ReactNode } from "react";
import { Card } from "@/components/ui/card";
import { EmptyState, ErrorState, LoadingState } from "../values/states";

/**
 * ListPage（query 块，本仓适配形）—— 列表卡体骨架：toolbar × 四态 × appendix。
 * 所有 part 都是 ReactNode（结构性逃生舱）：不满意任一部分就传自己的组件，
 * 只要消费 list 句柄。四态在此切换；DataTable 只渲染 success 表体。
 * 页壳与页头不在块内（本仓单源在 PageFrame/PageHeader）。
 */
export interface ListPageProps<T> {
  toolbar?: ReactNode;
  pagination?: ReactNode;
  appendix?: ReactNode;
  list: CollectionHandle<T>;
  emptyTitle?: string;
  emptyDescription?: string;
  emptyAction?: ReactNode;
  children: ReactNode; // success 态内容（通常 <DataTable list={list} …/>）
}

export function ListPage<T>({
  toolbar,
  pagination,
  appendix,
  list,
  emptyTitle = "Nothing here",
  emptyDescription,
  emptyAction,
  children,
}: ListPageProps<T>) {
  return (
    <div className="w-full space-y-4">
      <Card>
        {toolbar}
        {list.status === "loading" ? (
          <LoadingState />
        ) : list.status === "error" ? (
          <ErrorState error={list.error!} onRetry={list.refetch} />
        ) : list.status === "empty" ? (
          <EmptyState title={emptyTitle} description={emptyDescription} action={emptyAction} />
        ) : (
          children
        )}
        {list.status !== "loading" && list.status !== "error" ? pagination : null}
      </Card>
      {appendix}
    </div>
  );
}
