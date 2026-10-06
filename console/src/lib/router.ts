import { useCallback, useEffect, useState } from "react";

// 手写 hash 路由 v2（F3.1 写面升格）：F2.6 的三平坦路由扩展为带参数段
// （部署详情 #/deployments/<id>）。仍零依赖——路由库的需求（嵌套布局/
// 查询串）尚未出现，最小面原则维持手写（ADR-0044 决策 2 的升格条件是
// "需要 URL 参数"而非"需要路由库"）。

export const ROUTES = [
  "deployments",
  "apps",
  "resources",
  "tasks",
  "logs",
  "events",
  "audit",
  "settings",
  "quickstart",
] as const;
export type Route = (typeof ROUTES)[number];

// RouteView 是解析后的路由形态：page 是导航位（无参数时的路由名），
// detailId 是 deployments/<id> 的参数段（仅部署详情页消费）。
export interface RouteView {
  page: Route;
  detailId: string;
}

// parseHash 把 location.hash 解析为 RouteView（空/未知 = 默认部署页——
// SPA fallback 直达与手输 typo 都落到安全页）。
export function parseHash(hash: string): RouteView {
  const raw = hash.replace(/^#\/?/, "");
  const segments = raw.split("/").filter((part) => part !== "");
  const head = segments[0] ?? "";
  if ((ROUTES as readonly string[]).includes(head)) {
    return { page: head as Route, detailId: segments[1] ?? "" };
  }
  return { page: "deployments", detailId: "" };
}

// useHashRoute 订阅 hashchange 并返回 [当前视图, 导航函数]。
export function useHashRoute(): [RouteView, (path: string) => void] {
  const [view, setView] = useState<RouteView>(() => parseHash(window.location.hash));
  useEffect(() => {
    const onHashChange = () => setView(parseHash(window.location.hash));
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);
  const navigate = useCallback((path: string) => {
    window.location.hash = `#/${path.replace(/^\/?/, "")}`;
  }, []);
  return [view, navigate];
}
