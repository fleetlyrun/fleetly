import { Outlet, createRootRoute } from "@tanstack/react-router";

// 根路由：仅 Outlet。壳与鉴权门在 pathless 的 /_shell 布局路由，登录页
// 独立壳外——这是 UI v2 的鉴权拓扑（401 闭环见 api/client.ts）。
export const Route = createRootRoute({
  component: () => <Outlet />,
});
