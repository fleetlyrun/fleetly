import { useEffect, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useCollection, useRecord } from "@lynx-go/uikit-core";
import { z } from "zod";
import { RefreshCwIcon } from "lucide-react";
import { appsDirectorySearch, appsResource } from "@/kit/apps";
import { METRIC_RANGES, useRawMetricsSeries, useScopedMetricsSeries } from "@/kit/metrics";
import { sessionResource } from "@/kit/settings";
import { useProjectId } from "@/lib/project";
import { METRIC_PRESETS } from "@/features/metrics/metric-presets";
import { MetricChart } from "@/features/metrics/metric-chart";
import { ErrorState } from "@/components/domain/error-state";
import { PageTabs } from "@/components/domain/page-tabs";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";

// 指标工作台（Workbench 原型，UI v2 批 3；H8 阶段 B 切换）：预设走服务端
// scoped 模板（GET /v1/metrics/scoped——请求只带 app id + 指标白名单），
// 自定义 PromQL 保留为平台管理员诊断面（GET /v1/metrics 原始透传）。批 5
// 模板化复裁：Metric 下拉升格为 PageTabs 四 tab（CPU % / Memory / CPU
// cores / Custom PromQL，用户裁决 2026-10-11）——tab 即 preset search
// param，URL 契约不变；Custom tab 仅 whoami.platform_admin 可见，非管理员
// 深链 preset=custom 诚实回落到 CPU %（设计 §5.4）。根高
// calc(100svh-3.25rem)（顶栏 h-13）：workbench 视口定死，图区内部滚动。
// 2026-10-27 批4 读面收尾：App 目录走 kit apps 资源（选择器非列表语义，
// 目录级大页直接吃 rows，60s 目录轮询照 catalog 原值）、会话读走
// sessionResource record、时序读走 kit/metrics——URL 即真相页，search
// 逻辑零改动。

const metricsSearch = z.object({
  app: z.string().optional(),
  preset: z.string().optional(),
  range: z.string().optional(),
});

const METRIC_TABS = [
  { value: "cpu_percent", label: "CPU %" },
  { value: "memory", label: "Memory" },
  { value: "cpu_cores", label: "CPU cores" },
  { value: "custom", label: "Custom PromQL" },
] as const;

type MetricTab = (typeof METRIC_TABS)[number]["value"];

export const Route = createFileRoute("/_shell/metrics")({
  validateSearch: metricsSearch,
  component: MetricsWorkbench,
});

function MetricsWorkbench() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const [projectId] = useProjectId();
  // 选择器非列表语义：目录级大页直接吃 rows（scope 未就绪自动停用——
  // projectId 缺省不发请求）。60s 目录轮询照 lib/catalog 原值。
  const apps = useCollection(appsResource, {
    from: Route.id,
    search: appsDirectorySearch,
    scope: { projectId: projectId || undefined },
    query: { refetchInterval: 60_000 },
  });
  // 会话读经 sessionResource record（id 恒 "self"）；Custom PromQL 显隐只看
  // 服务端 platform_admin——Console 不复制 role/team 判定。
  const session = useRecord(sessionResource, { id: "self" });
  // URL 即真相（批1 §3 裁决）：app/preset/range 三选全进 search——原 useState
  // 持有 + 仅 range 回写的半吊子形态在回退/前进时失同步（设计路线所指
  // useState 不一致即此）。customQuery 是草稿输入，保持本地态。
  const appId = search.app ?? "";
  const presetKey = parseTab(search.preset);
  const rangeKey = search.range ?? "1h";
  const [customQuery, setCustomQuery] = useState("");

  // 项目目录到达后补默认 App（同日志页口径）：缺省选择写回 URL（replace）。
  useEffect(() => {
    if (search.app === undefined && apps.rows.length > 0) {
      void navigate({ replace: true, search: (prev) => ({ ...prev, app: apps.rows[0].id }) });
    }
  }, [apps.rows, search.app, navigate]);

  // Custom PromQL 是平台管理员诊断面：显隐只看服务端 platform_admin，
  // Console 不复制 role/team 判定。
  const platformAdmin = session.data?.platformAdmin === true;
  const allowedTabs = platformAdmin ? METRIC_TABS : METRIC_TABS.filter((tab) => tab.value !== "custom");
  const activeTab: MetricTab = presetKey === "custom" && !platformAdmin ? "cpu_percent" : presetKey;
  const preset = METRIC_PRESETS.find((entry) => entry.key === activeTab) ?? METRIC_PRESETS[0];
  const custom = activeTab === "custom";
  const scopedAppId = appId || apps.rows[0]?.id || "";
  const rawQuery = custom ? customQuery.trim() : "";

  // 两钩子都无条件调用；scoped 钩子在 custom 模式下 enabled=false（不产生
  // 后台预设请求），raw 钩子在非 custom 模式 query 为空不发请求。
  const presetSeries = useScopedMetricsSeries({ kind: "app", id: scopedAppId }, preset.metric, undefined, rangeKey, !custom);
  const rawSeries = useRawMetricsSeries(rawQuery, rangeKey);
  const series = custom ? rawSeries : presetSeries;
  const seriesList = custom ? (rawSeries.data ?? []) : (presetSeries.data?.series ?? []);
  const hasQuery = custom ? rawQuery !== "" : scopedAppId !== "";
  const unit = custom ? "plain" : preset.unit;

  function update(next: { app?: string; preset?: string; range?: string }) {
    void navigate({ replace: true, search: (prev) => ({ ...prev, ...next }) });
  }

  return (
    <div className="flex h-[calc(100svh-3.25rem)] flex-col px-5 pt-4">
      <div className="mb-1 flex items-baseline gap-3">
        <h1 className="font-heading text-lg font-bold tracking-tight">Metrics</h1>
        <span className="text-xs text-muted">
          {custom
            ? "Raw PromQL diagnostics (platform admin) · refreshed every 30s"
            : "Scoped metric templates from managed VictoriaMetrics · refreshed every 30s"}
        </span>
      </div>

      <div className="flex flex-none flex-wrap items-end gap-2.5 py-3">
        <div className="flex flex-col gap-1">
          <Label className="text-2xs text-muted">App</Label>
          <Select value={appId} onValueChange={(value) => update({ app: value })}>
            <SelectTrigger className="w-44">
              <SelectValue placeholder="select an app…" />
            </SelectTrigger>
            <SelectContent>
              {apps.rows.map((app) => (
                <SelectItem key={app.id} value={app.id}>
                  {app.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-2xs text-muted">Range</Label>
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

      <PageTabs
        tabs={allowedTabs.map((tab) => ({ value: tab.value, label: tab.label }))}
        current={activeTab}
        onChange={(value) => {
          // tab 深链（URL 即真相）：默认 CPU % 不落键（保持 URL 干净），回退/前进同步。
          update({ preset: value === "cpu_percent" ? undefined : value });
        }}
      />

      {custom ? (
        <div className="mb-3 flex flex-none flex-col gap-1">
          <Label className="text-2xs text-muted">PromQL</Label>
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
        {series.isPending && hasQuery ? (
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            <Skeleton className="h-64 rounded-xl" />
            <Skeleton className="h-64 rounded-xl" />
          </div>
        ) : series.isError ? (
          <ErrorState error={series.error} onRetry={() => void series.refetch()} />
        ) : seriesList.length > 0 ? (
          <Card>
            <CardHeader className="pb-2">
              <CardTitle className="text-sm">
                {custom ? "Custom query" : preset.label}
                <span className="ml-2 text-2xs font-normal text-muted">
                  {seriesList.length} series · step {Math.max(15, Math.round((METRIC_RANGES[rangeKey] ?? 3_600_000) / 1000 / 240))}s
                </span>
              </CardTitle>
            </CardHeader>
            <CardContent>
              <MetricChart
                seriesList={seriesList}
                unit={unit}
                truncated={custom ? undefined : presetSeries.data?.truncated}
                seriesTotal={custom ? undefined : presetSeries.data?.seriesTotal}
              />
            </CardContent>
          </Card>
        ) : hasQuery ? (
          <div className="rounded-xl border border-dashed px-6 py-10 text-center text-sm text-muted">
            No series matched — pick an app with running processes, or widen the range.
          </div>
        ) : custom ? (
          <div className="rounded-xl border border-dashed px-6 py-10 text-center text-sm text-muted">
            Type a PromQL query above — series chart as you type.
          </div>
        ) : (
          <div className="rounded-xl border border-dashed px-6 py-10 text-center text-sm text-muted">
            Pick an app to chart — CPU and memory presets come preloaded.
          </div>
        )}
      </div>
    </div>
  );
}

// parseTab 把 search param 收敛到 tab 联合（旧值/未知值落回 CPU %，URL
// 深链诚实降级）。
function parseTab(raw: string | undefined): MetricTab {
  return METRIC_TABS.some((tab) => tab.value === raw) ? (raw as MetricTab) : "cpu_percent";
}
