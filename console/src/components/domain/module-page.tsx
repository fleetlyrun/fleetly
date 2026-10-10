import type { ReactNode } from "react";
import { PageTabs } from "./page-tabs";
import { PageFrame } from "./page-frame";

// 复杂页模板（一页多模块，对齐批 5 裁决的模板化落点）：PageFrame +
// PageTabs + 模块槽。tab 深链（URL search param + zod validateSearch）
// 是路由类型面，由各路由自带；本件只收口壳/页头/tab 条的结构位次——
// 一页多模块页长得不一样是缺陷，模块内容各异是常态（模块用 ListCard
// 填充或特制内容）。lead 槽承接 tab 条之上的页级前置物（如 Alerts 的
// firing 摘要条）。
export function ModulePage<T extends string>({
  width,
  title,
  description,
  actions,
  lead,
  tabs,
  current,
  onTabChange,
  children,
}: {
  width?: "wide" | "narrow";
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  lead?: ReactNode;
  tabs: Array<{ value: T; label: string }>;
  current: T;
  onTabChange: (value: T) => void;
  children: ReactNode;
}) {
  return (
    <PageFrame width={width} title={title} description={description} actions={actions}>
      {lead}
      <PageTabs tabs={tabs} current={current} onChange={onTabChange} />
      {children}
    </PageFrame>
  );
}
