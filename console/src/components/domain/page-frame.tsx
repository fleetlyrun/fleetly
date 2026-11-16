import type { ReactNode } from "react";
import { cn } from "cn";
import { PageHeader } from "./page-header";

// PageFrame 是页面壳单源（六原型公共底座）：主内容列一处收口——手拼
// "mx-auto max-w-7xl px-6 pt-6 pb-8" 是反模式（守卫执法）。2026-10-28
// 机制收口后衬垫/页头全仓唯一宿主就是本件（ModulePage 经此组合）：kit
// ListPage 已零页面 chrome（壳内模块配方），一页一壳由组件形状保证。
// 2026-10-27 走查统一裁决：**主内容宽度流体全宽，全部页面同列**——
// 原 wide/narrow/flow 三档 max-w 与 kit ListPage 的流体容器并存造成
// 页面间宽度不一致（用户反馈），三档退役：width prop 保留签名兼容（不再
// 分档）。
// 2026-11-15 Dokploy 页面卡：外层 rounded-xl bg-sidebar p-2.5 承托内层
// rounded-xl 边框卡（bg-background），页头与内容都住内层卡里——页与壳的
// 层级只由本件表达，页面不再手拼卡片/衬垫。title 省略 = 纯容器（Detail
// 类页自带英雄卡）：breadcrumb 落内容区顶部（面包屑仍是壳槽，不归页面）；
// flush 时面包屑自带 px-6 pt-5，直接压在 hero 顶带上（hero 的 border-b
// 是全卡横线，不能被内容衬垫截断）。bodyClassName / flush 是详情英雄页
// 的逃生舱：flush 去掉默认 p-6，bodyClassName 追加自定义衬垫/布局。
export function PageFrame({
  width: _width = "wide",
  breadcrumb,
  title,
  description,
  actions,
  bodyClassName,
  flush = false,
  children,
}: {
  width?: "wide" | "narrow" | "flow";
  breadcrumb?: ReactNode;
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  /** 内容区附加类（与默认 p-6 合并；flush 时单独使用） */
  bodyClassName?: string;
  /** 详情英雄页去掉默认内容衬垫（页头/面包屑仍归壳） */
  flush?: boolean;
  children: ReactNode;
}) {
  void _width;
  return (
    <div className="flex min-h-full min-w-0 flex-col rounded-xl bg-sidebar p-2.5">
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden rounded-xl border border-line bg-background shadow-sm">
        {title != null ? (
          <PageHeader breadcrumb={breadcrumb} title={title} description={description} actions={actions} />
        ) : null}
        <div className={cn("flex min-w-0 flex-1 flex-col", !flush && "p-6", bodyClassName)}>
          {title == null && breadcrumb != null ? (
            <div className={flush ? "px-6 pt-5" : "mb-4"}>{breadcrumb}</div>
          ) : null}
          {children}
        </div>
      </div>
    </div>
  );
}
