// vendored from @lynx-go/uikit registry/query/list-page.tsx @0.3.1（2026-10-26 批0 底座重拷（demo 视觉原生落地））
// 拷贝适配（scripts/devtool.mjs --profile fleetly）：
// token 方言映射 本块无方言 token；cn 统一 @/lib/utils。
// 手工步骤（判断题，工具不自动改）：
// - 未映射方言 token text-muted ×1——每块语境判断（torchwood amber/emerald、fleetly status vars 先例）
// 本地扩展（升版重拷对照清单）：
// - 容器宽度 max-w-5xl → 流体全宽（数据密集 console 的列表有效宽度反馈
//   ——demo 方言的窄容器在宽屏留白过大）。
// - 页面 chrome 退役（2026-10-28 机制收口）：外层 padding（px-6 py-6）与
//   页头三件（title/description/actions）不再是本件职责——一页一壳，壳 =
//   PageFrame/ModulePage 单源（domain），本件降为壳内模块配方（卡体 +
//   工具栏 + 四态 + appendix）。双衬垫/双页头从组件形状上不可能发生；
//   升版重拷时上游的 padding 与 header 块不再回拷。执法：
//   console.ui.antipattern.test.ts kit chrome 禁令。
// 其余与上游一致。
import type { CollectionHandle } from "@lynx-go/uikit-core/react";
import type { ApiError } from "@lynx-go/uikit-core/kernel";
import type { ReactNode } from "react";
import { Card } from "@/components/ui/card";
import { EmptyState, ErrorState, LoadingState } from "../values/states";

/**
 * ListPage（query 块）—— 壳内列表模块配方：toolbar × 四态 × appendix。
 * 所有 part 都是 ReactNode（结构性逃生舱）：不满意任一部分就传自己的组件，
 * 只要消费 list 句柄。四态在此切换；DataTable 只渲染 success 表体。
 * 页面壳与页头不归本件（fleetly 分叉）：外层必须是 PageFrame/ModulePage
 * （一页一壳），页头三件走壳的 props。错误态文案透传（describeError/
 * title/retryLabel，§12.10 余款）：消费方 describeApiError 单源经此注入，
 * states 块免于逐副本中文化。
 */
export interface ListPageProps<T> {
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
  return (
    <div className="w-full space-y-4">
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
