import type { ReactNode } from "react";
import { cn } from "cn";
import { PageHeader } from "./page-header";

// PageFrame 是页面壳单源（六原型公共底座）：宽容器 + PageHeader 一处收口
// ——手拼 "mx-auto max-w-7xl px-6 pt-6 pb-8" 是反模式（守卫执法）。width
// 两档：wide=列表/工作台默认；narrow=Settings 类表单区块页。ListPage /
// ModulePage 模板内部经此落壳；特殊形态页（Workbench/Dashboard/Detail）
// 直接消费本件。
export function PageFrame({
  width = "wide",
  breadcrumb,
  title,
  description,
  actions,
  children,
}: {
  width?: "wide" | "narrow";
  breadcrumb?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className={cn("mx-auto px-6 pt-6 pb-8", width === "narrow" ? "max-w-5xl" : "max-w-7xl")}>
      <PageHeader breadcrumb={breadcrumb} title={title} description={description} actions={actions} />
      {children}
    </div>
  );
}
