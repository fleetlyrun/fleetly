import type { ReactNode } from "react";
import { PageHeader } from "./page-header";

// PageFrame 是页面壳单源（六原型公共底座）：主内容列一处收口——手拼
// "mx-auto max-w-7xl px-6 pt-6 pb-8" 是反模式（守卫执法）。2026-10-28
// 机制收口后衬垫/页头全仓唯一宿主就是本件（ModulePage 经此组合）：kit
// ListPage 已零页面 chrome（壳内模块配方），一页一壳由组件形状保证。
// 2026-10-27 走查统一裁决：**主内容宽度流体全宽，全部页面同列**——
// 原 wide/narrow/flow 三档 max-w 与 kit kit ListPage 的流体容器并存造成
// 页面间宽度不一致（用户反馈），三档退役：width prop 保留签名兼容（不再
// 分档），容器统一 `w-full px-6 pt-6 pb-8`（与 kit ListPage/壳横向衬垫
// px-6 同族）。title 省略 = 纯容器（Detail 类页自带英雄卡）：此时
// breadcrumb 落壳内裸 crumb 槽（mb-3 接英雄卡）——不收槽位页面裸塞首
// 元素，上吃满 pt 下贴卡片（2026-10-12 走查实咬）。
export function PageFrame({
  width: _width = "wide",
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
  void _width;
  return (
    <div className="w-full px-6 pt-6 pb-8">
      {title != null ? (
        <PageHeader breadcrumb={breadcrumb} title={title} description={description} actions={actions} />
      ) : breadcrumb != null ? (
        <div className="mb-3">{breadcrumb}</div>
      ) : null}
      {children}
    </div>
  );
}
