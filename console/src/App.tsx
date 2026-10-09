import { lazy, Suspense } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { LoginPage } from "./pages/Login";
import { ROUTES, useHashRoute, type Route } from "./lib/router";
import { setToken, useToken } from "./lib/token";
import { useWhoami } from "./lib/catalog";

// 页面路由级代码分割（走查性能批：单 chunk 680KB 中 xterm 只有终端页
// 消费、react 全家桶是稳定 vendor——lazy 按页拆 + manualChunks 拆 vendor，
// 首屏 = 入口 + vendor + 当前页 chunk；hash 路由零改动）。LoginPage 静态
// 进口（首屏态，懒加载无收益）。
const DeploymentsPage = lazy(() => import("./pages/Deployments").then((m) => ({ default: m.DeploymentsPage })));
const DeploymentDetailPage = lazy(() => import("./pages/DeploymentDetail").then((m) => ({ default: m.DeploymentDetailPage })));
const LogsPage = lazy(() => import("./pages/Logs").then((m) => ({ default: m.LogsPage })));
const EventsPage = lazy(() => import("./pages/Events").then((m) => ({ default: m.EventsPage })));
const AppsPage = lazy(() => import("./pages/Apps").then((m) => ({ default: m.AppsPage })));
const ResourcesPage = lazy(() => import("./pages/Resources").then((m) => ({ default: m.ResourcesPage })));
const TasksPage = lazy(() => import("./pages/Tasks").then((m) => ({ default: m.TasksPage })));
const ObservabilityPage = lazy(() => import("./pages/Observability").then((m) => ({ default: m.ObservabilityPage })));
const IdentityPage = lazy(() => import("./pages/Identity").then((m) => ({ default: m.IdentityPage })));
const NodesPage = lazy(() => import("./pages/Nodes").then((m) => ({ default: m.NodesPage })));
const AuditPage = lazy(() => import("./pages/Audit").then((m) => ({ default: m.AuditPage })));
const SettingsPage = lazy(() => import("./pages/Settings").then((m) => ({ default: m.SettingsPage })));
const QuickstartPage = lazy(() => import("./pages/Quickstart").then((m) => ({ default: m.QuickstartPage })));
const TemplatesPage = lazy(() => import("./pages/Templates").then((m) => ({ default: m.TemplatesPage })));
const TerminalPage = lazy(() => import("./pages/Terminal").then((m) => ({ default: m.TerminalPage })));

// Console 外壳（F2.6 三页 → F3.1 全功能 → C1 可观测批 → C2 治理批）：无
// Token = 登录页；有 Token = 页头导航（十三路由）+ 身份栏（whoami + 登出）。
// 终端页在导航末位（F3.2 exec 子面）。
export function App() {
  const [token] = useToken();
  if (token === "") return <LoginPage />;
  return <Shell />;
}

function Shell() {
  const [view, navigate] = useHashRoute();
  return (
    <div className="flex min-h-screen flex-col">
      <header className="sticky top-0 z-10 flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-slate-800 bg-slate-950/95 px-4 py-2.5">
        <span className="text-sm font-semibold tracking-wide text-slate-100">fleetly</span>
        <nav className="flex flex-wrap gap-1">
          {ROUTES.map((candidate) => (
            <NavTab key={candidate} route={candidate} active={candidate === view.page} onNavigate={() => navigate(candidate)} />
          ))}
        </nav>
        <div className="ml-auto">
          <IdentityBar />
        </div>
      </header>
      <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6">
        <Suspense fallback={<p className="text-sm text-slate-500">Loading…</p>}>{renderPage(view.page, view.detailId, navigate)}</Suspense>
      </main>
    </div>
  );
}

function renderPage(page: Route, detailId: string, navigate: (path: string) => void) {
  switch (page) {
    case "deployments":
      return detailId === "" ? <DeploymentsPage /> : <DeploymentDetailPage id={detailId} navigate={navigate} />;
    case "apps":
      return <AppsPage />;
    case "resources":
      return <ResourcesPage />;
    case "tasks":
      return <TasksPage />;
    case "observability":
      return <ObservabilityPage />;
    case "identity":
      return <IdentityPage />;
    case "nodes":
      return <NodesPage />;
    case "logs":
      return <LogsPage />;
    case "events":
      return <EventsPage />;
    case "audit":
      return <AuditPage />;
    case "settings":
      return <SettingsPage />;
    case "quickstart":
      return <QuickstartPage />;
    case "templates":
      return <TemplatesPage navigate={navigate} />;
    case "terminal":
      return <TerminalPage />;
  }
}

const ROUTE_LABELS: Record<Route, string> = {
  deployments: "Deployments",
  apps: "Apps",
  resources: "Resources",
  tasks: "Tasks",
  observability: "Observability",
  identity: "Identity",
  nodes: "Nodes",
  logs: "Logs",
  events: "Events",
  audit: "Audit",
  settings: "Settings",
  quickstart: "Quickstart",
  templates: "Templates",
  terminal: "Terminal",
};

function NavTab({ route, active, onNavigate }: { route: Route; active: boolean; onNavigate: () => void }) {
  return (
    <button
      type="button"
      onClick={onNavigate}
      className={
        active
          ? "rounded-md bg-slate-800 px-3 py-1.5 text-sm font-medium text-slate-100"
          : "rounded-md px-3 py-1.5 text-sm text-slate-400 hover:bg-slate-900 hover:text-slate-200"
      }
    >
      {ROUTE_LABELS[route]}
    </button>
  );
}

// IdentityBar：whoami 身份 + 登出（清 Token 失效全部查询——回登录页）。
function IdentityBar() {
  const whoami = useWhoami(true);
  const queryClient = useQueryClient();
  return (
    <div className="flex items-center gap-3 text-xs text-slate-500">
      {whoami.data ? (
        <span>
          <span className="text-slate-300">{whoami.data.tokenName}</span>
          <span className="mx-1.5">·</span>
          <span>{whoami.data.roleName}</span>
        </span>
      ) : null}
      <button
        type="button"
        onClick={() => {
          setToken("");
          void queryClient.invalidateQueries();
        }}
        className="rounded-md border border-slate-700 px-2.5 py-1 text-xs text-slate-400 hover:border-slate-500 hover:text-slate-200"
      >
        Sign out
      </button>
    </div>
  );
}
