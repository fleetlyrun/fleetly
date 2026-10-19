import { useState } from "react";
import { METRIC_PRESETS, type MetricPreset } from "@/features/metrics/metric-presets";
import { MetricChart } from "@/features/metrics/metric-chart";
import { useRevisions, useScopedMetricsSeries, type ScopedMetric } from "@/lib/catalog";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

// App Metrics scoped tab（IA v3 T2；H8 阶段 B 起走服务端 scoped 模板）：
// 请求只带 app id + 独立预设的指标白名单 + 可选 process，PromQL 由服务端
// 按行解析出的 Project 固定构造（设计 §5.4）。process 选择器是 per-process
// 下钻（2026-10-12 层级定案：观测类 tab 可下探 Process；Instance 级留给
// 工作台自定义 PromQL）——"all" = 全进程并集（原语义）。
export function AppMetricsTab({ appId }: { appId: string }) {
  const revisions = useRevisions(appId);
  const processes = (revisions.data?.[0]?.process_strategies ?? [])
    .map((entry) => entry?.process ?? "")
    .filter((name) => name !== "");
  const [process, setProcess] = useState("all");
  const processFilter = process === "all" ? undefined : process;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end gap-2.5">
        <div className="flex flex-col gap-1">
          <Label className="text-2xs text-muted-foreground">Process</Label>
          <Select value={process} onValueChange={setProcess}>
            <SelectTrigger className="w-44">
              <SelectValue placeholder="—" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">
                all <span className="text-muted-foreground">(processes combined)</span>
              </SelectItem>
              {processes.map((name) => (
                <SelectItem key={name} value={name}>
                  {name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        {METRIC_PRESETS.filter((preset) => preset.key !== "cpu_cores").map((preset) => (
          <PresetCard
            key={preset.key}
            appId={appId}
            title={preset.label}
            unit={preset.unit}
            metric={preset.metric}
            process={processFilter}
          />
        ))}
      </div>
      <p className="text-2xs text-muted-foreground">
        Scope: app <span className="font-mono">{appId}</span>
        {process !== "all" ? (
          <>
            {" "}
            · process <span className="font-mono">{process}</span>
          </>
        ) : null}{" "}
        · window 1h · refreshed 30s · cross-app queries in the global{" "}
        <a className="text-info underline-offset-2 hover:underline" href="/metrics">
          Metrics workbench
        </a>
        .
      </p>
    </div>
  );
}

function PresetCard({
  appId,
  title,
  unit,
  metric,
  process,
}: {
  appId: string;
  title: string;
  unit: MetricPreset["unit"];
  metric: ScopedMetric;
  process: string | undefined;
}) {
  const series = useScopedMetricsSeries({ kind: "app", id: appId }, metric, process, "1h");
  const seriesList = series.data?.series ?? [];
  return (
    <section className="rounded-xl border bg-card p-4">
      <div className="mb-2 flex items-baseline justify-between">
        <h3 className="text-sm font-semibold">{title}</h3>
        <span className="text-2xs text-muted-foreground">window 1h · refreshed 30s</span>
      </div>
      {series.isPending ? (
        <p className="py-10 text-center text-xs text-muted-foreground">Loading series…</p>
      ) : series.isError ? (
        <p className="py-10 text-center text-xs text-destructive">{series.error instanceof Error ? series.error.message : String(series.error)}</p>
      ) : seriesList.length === 0 ? (
        <p className="py-10 text-center text-xs text-muted-foreground">No series — replicas may be stopped.</p>
      ) : (
        <MetricChart
          seriesList={seriesList}
          unit={unit}
          truncated={series.data?.truncated}
          seriesTotal={series.data?.seriesTotal}
        />
      )}
    </section>
  );
}
