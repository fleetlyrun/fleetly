// 应用列表：卡片网格——name、derived_state 徽章（degraded/blocked 一等
// 展示）、最近部署时间。点击进详情。

import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { ChevronRight, RefreshCw } from "lucide-react";

import { listApps } from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { StateBadge } from "@/components/state-badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { timeAgo } from "@/lib/utils";

export function AppsPage() {
  const query = useQuery({ queryKey: ["apps"], queryFn: listApps, refetchInterval: 5000 });

  if (query.isPending) {
    return <p className="text-sm text-muted-foreground">Loading apps…</p>;
  }
  if (query.isError) {
    const envelope = errorEnvelopeFrom(query.error);
    return (
      <div className="space-y-3">
        <h1 className="text-lg font-semibold">Applications</h1>
        <EnvelopeAlert
          code={envelope.code}
          message={envelope.message}
          suggestion={envelope.suggestion}
          docs={envelope.docs}
        />
      </div>
    );
  }

  // 生成类型口径：空 repeated 字段不出现在 JSON（EmitUnpopulated=false）。
  const apps = query.data.apps ?? [];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">Applications</h1>
        <Button
          variant="outline"
          size="sm"
          onClick={() => void query.refetch()}
          aria-label="Refresh apps"
        >
          <RefreshCw aria-hidden className="h-3.5 w-3.5" />
          Refresh
        </Button>
      </div>
      {apps.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No applications yet. Deploy a compose file to create the first app.
        </p>
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {apps.map((app) => (
            <Link
              key={app.id}
              to={`/apps/${encodeURIComponent(app.name ?? "")}`}
              className="group"
              data-testid="app-card"
            >
              <Card className="h-full transition-colors group-hover:border-foreground/30">
                <CardHeader className="flex-row items-center justify-between space-y-0 pb-2">
                  <CardTitle className="text-base">{app.name}</CardTitle>
                  <StateBadge state={app.derived_state ?? ""} />
                </CardHeader>
                <CardContent className="flex items-center justify-between text-xs text-muted-foreground">
                  <span>
                    updated {timeAgo(app.updated_at)}
                    {app.lifecycle !== "active" ? ` · ${app.lifecycle}` : ""}
                  </span>
                  <ChevronRight
                    aria-hidden
                    className="h-4 w-4 opacity-0 transition-opacity group-hover:opacity-100"
                  />
                </CardContent>
              </Card>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
