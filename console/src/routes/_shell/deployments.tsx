import { createFileRoute } from "@tanstack/react-router";
import { DeploymentsPage } from "@/pages/Deployments";

// 过渡路由（UI v2 批 1）：部署列表旧页挂新壳；页内 hash 直写在批 2 随
// 页面重构一并消灭（反模式守卫范围届时覆盖 pages/**）。
export const Route = createFileRoute("/_shell/deployments")({
  component: DeploymentsPage,
});
