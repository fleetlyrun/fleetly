import type { ReactNode } from "react";
import { cn } from "cn";
import { PageHeader } from "./page-header";

// PageFrame 是页面壳单源（六原型公共底座）：宽容器 + 页头一处收口——
// 手拼 "mx-auto max-w-7xl px-6 pt-6 pb-8" 是反模式（守卫执法）。width
// 三档对齐六原型：wide=Dashboard/List/Workbench 默认；narrow=Settings
// 表单区块页；flow=Flow 类多步页。title 省略 = 纯容器（Detail 类页自带
// 英雄卡）：此时 breadcrumb 落壳内裸 crumb 槽（mb-3 接英雄卡）——不收
// 槽位页面裸塞首元素，上吃满 pt 下贴卡片（2026-10-12 走查实咬）。
// ListPage/ModulePage 模板内部经此落壳。
export function PageFrame({
  width = "wide",
  breadcrumb,
  title,
  description,
  actions,
  children,
}: {
  width?: "wide" | "narrow" | "flow";
  breadcrumb?: ReactNode;
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
}) {
  const maxW = width === "narrow" ? "max-w-5xl" : width === "flow" ? "max-w-3xl" : "max-w-7xl";
  return (
    <div className={cn("mx-auto px-7 pt-7 pb-10", maxW)}>
      {title != null ? (
        <PageHeader breadcrumb={breadcrumb} title={title} description={description} actions={actions} />
      ) : breadcrumb != null ? (
        <div className="mb-3">{breadcrumb}</div>
      ) : null}
      {children}
    </div>
  );
}
