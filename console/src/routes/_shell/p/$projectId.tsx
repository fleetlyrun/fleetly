import { Outlet, createFileRoute } from "@tanstack/react-router";

// 项目语境布局（UI v2 信息架构）：/p/$projectId/** 的公共父级。项目存在
// 性由子页的目录查询诚实呈现（不在目录 = Not found 态），这里只承载语境。
export const Route = createFileRoute("/_shell/p/$projectId")({
  component: () => <Outlet />,
});
