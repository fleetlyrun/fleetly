import { useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { DeploymentsPage } from "./pages/Deployments";
import { EventsPage } from "./pages/Events";
import { LogsPage } from "./pages/Logs";
import { ROUTES, type Route, useHashRoute } from "./lib/router";
import { useToken } from "./lib/token";

// 最小只读 Console（F2.6/ADR-0044）：三页外壳——页头导航 + Token 栏。
export function App() {
  const [route, navigate] = useHashRoute();
  return (
    <div className="flex min-h-screen flex-col">
      <header className="sticky top-0 z-10 flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-slate-800 bg-slate-950/95 px-4 py-2.5">
        <span className="text-sm font-semibold tracking-wide text-slate-100">fleetly</span>
        <nav className="flex gap-1">
          {ROUTES.map((candidate) => (
            <NavTab key={candidate} route={candidate} active={candidate === route} onNavigate={navigate} />
          ))}
        </nav>
        <div className="ml-auto">
          <TokenBar />
        </div>
      </header>
      <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6">{renderPage(route)}</main>
    </div>
  );
}

function renderPage(route: Route) {
  switch (route) {
    case "deployments":
      return <DeploymentsPage />;
    case "logs":
      return <LogsPage />;
    case "events":
      return <EventsPage />;
  }
}

const ROUTE_LABELS: Record<Route, string> = {
  deployments: "Deployments",
  logs: "Logs",
  events: "Events",
};

function NavTab({ route, active, onNavigate }: { route: Route; active: boolean; onNavigate: (route: Route) => void }) {
  return (
    <button
      type="button"
      onClick={() => onNavigate(route)}
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

// TokenBar：Bearer Token 输入（localStorage 持久化）；保存即失效全部
// 查询让各页以新凭证重取。401 时各页错误态呈现。
function TokenBar() {
  const [saved, setSaved] = useToken();
  const [draft, setDraft] = useState(saved);
  const queryClient = useQueryClient();
  const inputId = useId();
  const changed = draft !== saved;
  return (
    <form
      className="flex items-center gap-2"
      onSubmit={(event) => {
        event.preventDefault();
        setSaved(draft);
        void queryClient.invalidateQueries();
      }}
    >
      <label htmlFor={inputId} className="text-xs text-slate-500">
        API token
      </label>
      <input
        id={inputId}
        type="password"
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        placeholder="paste a fleetly token"
        spellCheck={false}
        className="w-56 rounded-md border border-slate-700 bg-slate-900 px-2 py-1 font-mono text-xs text-slate-200 placeholder:text-slate-600 focus:border-sky-600 focus:outline-none"
      />
      <button
        type="submit"
        disabled={!changed}
        className="rounded-md border border-sky-700 bg-sky-900/40 px-2.5 py-1 text-xs font-medium text-sky-300 disabled:opacity-40"
      >
        Save
      </button>
    </form>
  );
}
