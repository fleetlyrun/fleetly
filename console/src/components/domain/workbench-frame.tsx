import type { ReactNode } from "react";
import { PageFrame } from "./page-frame";

// WorkbenchFrame（2026-11-16 Dokploy 收口）：日志/指标/终端等工作台不再是
// 页卡外的裸全幅面，而是与列表/详情同源的 PageFrame 页卡——标题条与内容
// 区仍保持工作台的全高/内部滚动语义。页面只给标题/描述与内容，不再手拼
// 外层高度链。
export function WorkbenchFrame({
  title,
  description,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
}) {
  return (
    <PageFrame flush>
      <div className="flex min-h-0 flex-1 flex-col">
        <header className="flex flex-none flex-wrap items-baseline gap-3 border-b border-line px-6 py-4">
          <h1 className="font-heading text-2xl font-semibold tracking-tight">{title}</h1>
          {description ? <p className="text-xs text-muted">{description}</p> : null}
        </header>
        <div className="flex min-h-0 flex-1 flex-col p-6">{children}</div>
      </div>
    </PageFrame>
  );
}
