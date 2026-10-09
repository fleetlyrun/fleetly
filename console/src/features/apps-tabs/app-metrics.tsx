import { METRIC_PRESETS } from "@/features/metrics/metric-presets";
import { MetricChart } from "@/features/metrics/metric-chart";
import { useMetricsSeries } from "@/lib/catalog";

// App Metrics scoped tab（IA v3 T2）：工作台预设的 app 域变体——归一标签
// 口径与全局工作台一致（appId 小写 = swarm sanitizeNamePart 的 label 值，
// metrics.tsx:51 同款）。CPU percent 的 group_left 除法语义见 metric-presets
// （F-B1：裸 on(node) 被 VM 拒 422）。
export function AppMetricsTab({ appId }: { appId: string }) {
  const appLabel = appId.toLowerCase();
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 lg:grid-cols-2">
        {METRIC_PRESETS.filter((preset) => preset.key !== "cpu_cores").map((preset) => (
          <PresetCard key={preset.key} title={preset.label} unit={preset.unit} query={preset.build(appLabel)} />
        ))}
      </div>
      <p className="text-[11.5px] text-muted-foreground">
        Scope: app <span className="font-mono">{appId}</span> · window 1h · refreshed 30s · cross-app queries in the global{" "}
        <a className="text-info underline-offset-2 hover:underline" href="/metrics">
          Metrics workbench
        </a>
        .
      </p>
    </div>
  );
}

function PresetCard({
  title,
  unit,
  query,
}: {
  title: string;
  unit: "percent" | "bytes" | "plain";
  query: string;
}) {
  const series = useMetricsSeries(query, "1h");
  return (
    <section className="rounded-xl border bg-card p-4">
      <div className="mb-2 flex items-baseline justify-between">
        <h3 className="text-[13px] font-semibold">{title}</h3>
        <span className="text-[11.5px] text-muted-foreground">window 1h · refreshed 30s</span>
      </div>
      {series.isPending ? (
        <p className="py-10 text-center text-xs text-muted-foreground">Loading series…</p>
      ) : series.isError ? (
        <p className="py-10 text-center text-xs text-destructive">{series.error instanceof Error ? series.error.message : String(series.error)}</p>
      ) : (series.data ?? []).length === 0 ? (
        <p className="py-10 text-center text-xs text-muted-foreground">No series — replicas may be stopped.</p>
      ) : (
        <MetricChart seriesList={series.data ?? []} unit={unit} />
      )}
    </section>
  );
}
