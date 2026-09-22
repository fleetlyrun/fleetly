// 应用详情壳：标题行（名称 + 派生状态 + 生命周期）+ 分段式子导航
// （概览/部署/日志/env/域名）+ 详情数据加载。深链形如
// /ui/apps/<name>/deployments——daemon 的 SPA 回退直接可达。

import { useQuery } from "@tanstack/react-query";
import { Boxes } from "lucide-react";
import { Outlet, useLocation, useNavigate, useParams } from "react-router-dom";

import { getApp } from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { PillTabs } from "@/components/pill-tabs";
import { StateBadge } from "@/components/state-badge";
import { timeAgo } from "@/lib/utils";

const TABS = [
  { key: "", label: "Overview" },
  { key: "deployments", label: "Deployments" },
  { key: "logs", label: "Logs" },
  { key: "env", label: "Env" },
  { key: "secrets", label: "Secrets" },
  { key: "domains", label: "Domains" },
];

export function AppDetailLayout() {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const location = useLocation();
  const query = useQuery({
    queryKey: ["app", name],
    queryFn: () => getApp(name),
    refetchInterval: 5000,
  });

  const base = `/apps/${encodeURIComponent(name)}`;
  const current =
    TABS.find((t) => t.key !== "" && location.pathname.startsWith(`${base}/${t.key}`))?.key ?? "";

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

  const app = query.data;
  const lifecycle = app?.lifecycle ?? "active";

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg border bg-muted/40">
          <Boxes aria-hidden className="h-5 w-5 text-muted-foreground" />
        </span>
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2.5">
            <h1 className="truncate font-mono text-xl font-semibold tracking-tight">{name}</h1>
            {app ? <StateBadge state={app.derived_state ?? ""} /> : null}
            {app && lifecycle !== "active" ? (
              <span className="rounded-md border px-1.5 py-0.5 text-xs text-muted-foreground">
                {lifecycle}
              </span>
            ) : null}
          </div>
          {app ? (
            <p className="text-xs text-muted-foreground">
              updated {timeAgo(app.updated_at)} · created {timeAgo(app.created_at)}
            </p>
          ) : null}
        </div>
      </div>

      <PillTabs
        ariaLabel="App sections"
        value={current}
        onValueChange={(key) => navigate(key === "" ? base : `${base}/${key}`)}
        items={TABS}
      />

      <Outlet />
    </div>
  );
}
