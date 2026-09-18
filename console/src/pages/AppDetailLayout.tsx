// 应用详情壳：子导航（概览/部署/日志/env/域名）+ 详情数据加载。
// 深链形如 /ui/apps/<name>/deployments——daemon 的 SPA 回退直接可达。

import { useQuery } from "@tanstack/react-query";
import { NavLink, Outlet, useParams } from "react-router-dom";

import { getApp } from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { StateBadge } from "@/components/state-badge";
import { cn } from "@/lib/utils";

const TABS = [
  { to: "", label: "Overview", end: true },
  { to: "deployments", label: "Deployments" },
  { to: "logs", label: "Logs" },
  { to: "env", label: "Env" },
  { to: "domains", label: "Domains" },
];

export function AppDetailLayout() {
  const { name = "" } = useParams();
  const query = useQuery({
    queryKey: ["app", name],
    queryFn: () => getApp(name),
    refetchInterval: 5000,
  });

  if (query.isError) {
    const envelope = errorEnvelopeFrom(query.error);
    return (
      <EnvelopeAlert
        code={envelope.code}
        message={envelope.message}
        suggestion={envelope.suggestion}
        docs={envelope.docs}
      />
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <h1 className="font-mono text-lg font-semibold">{name}</h1>
        {query.data ? <StateBadge state={query.data.derived_state} /> : null}
      </div>
      <nav className="flex gap-1 border-b" aria-label="App sections">
        {TABS.map(({ to, label, end }) => (
          <NavLink
            key={label}
            to={to}
            end={end}
            className={({ isActive }) =>
              cn(
                "-mb-px border-b-2 border-transparent px-3 py-2 text-sm font-medium text-muted-foreground hover:text-foreground",
                isActive && "border-foreground text-foreground",
              )
            }
          >
            {label}
          </NavLink>
        ))}
      </nav>
      <Outlet />
    </div>
  );
}
