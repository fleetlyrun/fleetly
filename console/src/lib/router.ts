import { useCallback, useEffect, useState } from "react";

// 手写 hash 路由（ADR-0044 决策 2）：三路由零依赖，N3 写面再升格路由库。
export const ROUTES = ["deployments", "logs", "events"] as const;
export type Route = (typeof ROUTES)[number];

// currentRoute 从 location.hash 提取路由名（空 = 默认部署状态页）。
function currentRoute(): Route {
  const raw = window.location.hash.replace(/^#\/?/, "");
  return (ROUTES as readonly string[]).includes(raw) ? (raw as Route) : "deployments";
}

// useHashRoute 订阅 hashchange 并返回 [当前路由, 导航函数]。
export function useHashRoute(): [Route, (route: Route) => void] {
  const [route, setRoute] = useState<Route>(currentRoute);
  useEffect(() => {
    const onHashChange = () => setRoute(currentRoute());
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);
  const navigate = useCallback((next: Route) => {
    window.location.hash = `#/${next}`;
  }, []);
  return [route, navigate];
}
