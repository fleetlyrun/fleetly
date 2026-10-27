// vendored from @lynx-go/uikit registry/query/list-page.tsx @0.3.1（2026-10-26 批0 底座重拷（demo 视觉原生落地））
// 拷贝适配（scripts/devtool.mjs --profile fleetly）：
// token 方言映射 本块无方言 token；cn 统一 @/lib/utils。
// 手工步骤（判断题，工具不自动改）：
// - 未映射方言 token text-muted ×1——每块语境判断（torchwood amber/emerald、fleetly status vars 先例）
// 本地扩展（2026-10-27 走查反馈，升版重拷对照）：
// - 容器宽度 max-w-5xl → 流体全宽 + px-6（数据密集 console 的列表有效
//   宽度反馈——demo 方言的窄容器在宽屏留白过大）
// 其余与上游一致。
import type { CollectionHandle } from "@lynx-go/uikit-core/react";
import type { ApiError } from "@lynx-go/uikit-core/kernel";
import type { ReactNode } from "react";
import { Card } from "@/components/ui/card";
import { EmptyState, ErrorState, LoadingState } from "../values/states";

/**
 * ListPage（query 块）—— 列表页配方骨架：header × toolbar × 四态 × appendix。
 * 所有 part 都是 ReactNode（结构性逃生舱）：不满意任一部分就传自己的组件，
 * 只要消费 list 句柄。四态在此切换；DataTable 只渲染 success 表体。
 * header 可选（title/actions 全缺省时不渲染——消费方自有页头架构时
 * shell-agnostic 化，§12.10-⑤；fleetly/torchwood 先例回流）。
 * 错误态文案透传（describeError/title/retryLabel，§12.10 余款）：消费方
 * describeApiError 单源经此注入，states 块免于逐副本中文化。
 */
export interface ListPageProps<T> {
  title?: string;
  description?: string;
  actions?: ReactNode;
  toolbar?: ReactNode;
  pagination?: ReactNode;
  appendix?: ReactNode;
  list: CollectionHandle<T>;
  emptyTitle?: string;
  emptyDescription?: string;
  emptyAction?: ReactNode;
  describeError?: (error: ApiError) => string;
  errorTitle?: string;
  retryLabel?: string;
  children: ReactNode; // success 态内容（通常 <DataTable list={list} …/>）
}

export function ListPage<T>({
  title,
  description,
  actions,
  toolbar,
  pagination,
  appendix,
  list,
  emptyTitle = "Nothing here",
  emptyDescription,
  emptyAction,
  describeError,
  errorTitle,
  retryLabel,
  children,
}: ListPageProps<T>) {
  const showHeader = title !== undefined || description !== undefined || actions !== undefined;
  return (
    <div className="w-full space-y-4 px-6 py-6">
      {showHeader ? (
        <header className="flex items-start justify-between gap-4">
          <div>
            {title ? <h1 className="text-lg font-semibold">{title}</h1> : null}
            {description ? <p className="mt-0.5 text-xs text-muted">{description}</p> : null}
          </div>
          {actions ? <div className="flex items-center gap-2">{actions}</div> : null}
        </header>
      ) : null}
      <Card>
        {toolbar}
        {list.status === "loading" ? (
          <LoadingState />
        ) : list.status === "error" ? (
          <ErrorState
            error={list.error!}
            onRetry={list.refetch}
            describeError={describeError}
            title={errorTitle}
            retryLabel={retryLabel}
          />
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
