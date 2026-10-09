import { useEffect, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { RefreshCwIcon } from "lucide-react";
import { METRIC_RANGES, useApps, useMetricsSeries } from "@/lib/catalog";
import { useProjectId } from "@/lib/project";
import { METRIC_PRESETS } from "@/features/metrics/metric-presets";
import { MetricChart } from "@/features/metrics/metric-chart";
import { ErrorState } from "@/components/domain/error-state";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";

// 指标工作台（Workbench 原型，UI v2 批 3）：预设/自定义 PromQL + 时间窗
// （search param）+ shadcn Chart。查询经 GET /v1/metrics 透传（无 WS——
// 前端 30s 轮询是既有契约）。

const metricsSearch = z.object({
  app: z.string().optional(),
  preset: z.string().optional(),
  range: z.string().optional(),
});

export const Route = createFileRoute("/_shell/metrics")({
  validateSearch: metricsSearch,
  component: MetricsWorkbench,
});

function MetricsWorkbench() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const [projectId] = useProjectId();
  const apps = useApps(projectId);
  const [appId, setAppId] = useState(search.app ?? "");
  const [presetKey, setPresetKey] = useState(search.preset ?? "cpu_percent");
  const [rangeKey, setRangeKey] = useState(search.range ?? "1h");
  const [customQuery, setCustomQuery] = useState("");
  const [customOpen, setCustomOpen] = useState(false);

  // 项目目录到达后补默认 App（同日志页口径）
  useEffect(() => {
    if (appId === "" && search.app === undefined && (apps.data ?? []).length > 0) {
      setAppId(apps.data![0].id);
    }
  }, [apps.data, appId, search.app]);

  const preset = METRIC_PRESETS.find((entry) => entry.key === presetKey) ?? METRIC_PRESETS[0];
  const appLabel = (appId || (apps.data ?? [])[0]?.id || "").toLowerCase();
  const query = customOpen ? customQuery.trim() : appId === "" ? "" : preset.build(appLabel);
  const unit = customOpen ? "plain" : preset.unit;
  const series = useMetricsSeries(query, rangeKey);

  function update(next: { app?: string; preset?: string; range?: string }) {
    if (next.app !== undefined) setAppId(next.app);
    if (next.preset !== undefined) setPresetKey(next.preset);
    if (next.range !== undefined) {
      setRangeKey(next.range);
      void navigate({ search: { app: appId || undefined, preset: presetKey, range: next.range } });
    }
  }

  return (
    <div className="flex h-full flex-col px-5 pt-4">
      <div className="mb-1 flex items-baseline gap-3">
        <h1 className="font-heading text-[17px] font-bold tracking-tight">Metrics</h1>
        <span className="text-xs text-muted-foreground">PromQL passthrough to managed VictoriaMetrics · refreshed every 30s</span>
      </div>

      <div className="flex flex-none flex-wrap items-end gap-2.5 py-3">
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">App</Label>
          <Select value={appId} onValueChange={(value) => update({ app: value })}>
            <SelectTrigger className="w-44">
              <SelectValue placeholder="select an app…" />
            </SelectTrigger>
            <SelectContent>
              {(apps.data ?? []).map((app) => (
                <SelectItem key={app.id} value={app.id}>
                  {app.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Metric</Label>
          <Select
            value={customOpen ? "custom" : presetKey}
            onValueChange={(value) => {
              if (value === "custom") setCustomOpen(true);
              else {
                setCustomOpen(false);
                update({ preset: value });
              }
            }}
          >
            <SelectTrigger className="w-56">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {METRIC_PRESETS.map((entry) => (
                <SelectItem key={entry.key} value={entry.key}>
                  {entry.label}
                </SelectItem>
              ))}
              <SelectItem value="custom">Custom PromQL…</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Range</Label>
          <Select value={rangeKey} onValueChange={(value) => update({ range: value })}>
            <SelectTrigger className="w-24">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {Object.keys(METRIC_RANGES).map((key) => (
                <SelectItem key={key} value={key}>
                  {key}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Button variant="ghost" size="icon-sm" className="mb-0.5" onClick={() => void series.refetch()} aria-label="Refresh">
          <RefreshCwIcon className="size-3.5" />
        </Button>
      </div>

      {customOpen ? (
        <div className="mb-3 flex flex-none flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">PromQL</Label>
          <Input
            value={customQuery}
            onChange={(event) => setCustomQuery(event.target.value)}
            placeholder='rate(container_cpu_usage_seconds_total[2m])'
            className="h-8 max-w-xl font-mono text-xs"
            spellCheck={false}
          />
        </div>
      ) : null}

      <div className="min-h-0 flex-1 overflow-y-auto pb-5">
        {series.isPending && query !== "" ? (
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            <Skeleton className="h-64 rounded-xl" />
            <Skeleton className="h-64 rounded-xl" />
          </div>
        ) : series.isError ? (
          <ErrorState error={series.error} onRetry={() => void series.refetch()} />
        ) : series.data != null && series.data.length > 0 ? (
          <Card>
            <CardHeader className="pb-2">
              <CardTitle className="text-[13px]">
                {customOpen ? "Custom query" : preset.label}
                <span className="ml-2 text-[11px] font-normal text-muted-foreground">
                  {series.data.length} series · step {Math.max(15, Math.round((METRIC_RANGES[rangeKey] ?? 3_600_000) / 1000 / 240))}s
                </span>
              </CardTitle>
            </CardHeader>
            <CardContent>
              <MetricChart seriesList={series.data} unit={unit} />
            </CardContent>
          </Card>
        ) : query !== "" ? (
          <div className="rounded-xl border border-dashed px-6 py-10 text-center text-sm text-muted-foreground">
            No series matched — pick an app with running processes, or widen the range.
          </div>
        ) : (
          <div className="rounded-xl border border-dashed px-6 py-10 text-center text-sm text-muted-foreground">
            Pick an app to chart — CPU and memory presets come preloaded.
          </div>
        )}
      </div>
    </div>
  );
}
