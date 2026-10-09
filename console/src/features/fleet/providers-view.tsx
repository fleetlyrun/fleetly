import { ActivityIcon, GlobeIcon, LayersIcon, ScrollTextIcon } from "lucide-react";
import { METRICS_FRESHNESS_QUERY, formatFreshness, lastPointValue } from "@/features/fleet/freshness";
import { componentHealth, useSystemStatus } from "@/features/fleet/use-system-status";
import { useMetricsSeries } from "@/lib/catalog";
import { PageHeader } from "@/components/domain/page-header";
import { StatusBadge, type StatusTone } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";

// Managed Providers 页（IA v3 T6，ADR-0058）：受管组件的排障驾驶舱。
// 页名采用既有词条 Managed Provider（CONTEXT.md；"component" 在其 Avoid
// 表——ADR-0058 记录该裁决），实名露出为 2026-10-09 拍板（推翻
// "managed proxy" 泛称）。
// 一期诚实边界（§5.2）：健康照实 unverified（GetStatus 恒 HEALTHY，
// provider Health() 二期接线）；endpoint 是 config 文件唯源不可读；
// Logs 侧 ingest 时效需 VL 查询代理（三期 ticket 面）。钉版数字是
// provider 编译期事实（受管 Workload 的镜像钉），非运行时探测。

interface ManagedProviderCard {
  key: string;
  name: string;
  icon: typeof GlobeIcon;
  role: string;
  pinned: string;
  freshness: "metrics-probe" | "needs-vl-proxy" | null;
  workbench?: { label: string; href: string };
}

const MANAGED_PROVIDERS: ManagedProviderCard[] = [
  { key: "traefik", name: "Traefik", icon: GlobeIcon, role: "Proxy — route publishing & TLS termination", pinned: "v3.5.4", freshness: null },
  { key: "zot", name: "zot", icon: LayersIcon, role: "Registry — build images, per-project credentials", pinned: "v2.1.21", freshness: null },
  {
    key: "victorialogs",
    name: "VictoriaLogs",
    icon: ScrollTextIcon,
    role: "Logging — ingest & retrieval",
    pinned: "v1.52.0",
    freshness: "needs-vl-proxy",
    workbench: { label: "Open Logs workbench", href: "/logs" },
  },
  {
    key: "victoriametrics",
    name: "VictoriaMetrics",
    icon: ActivityIcon,
    role: "Metrics — cadvisor collection & PromQL",
    pinned: "v1.152.0",
    freshness: "metrics-probe",
    workbench: { label: "Open Metrics workbench", href: "/metrics" },
  },
];

export function ManagedProvidersView() {
  const status = useSystemStatus();
  const degraded = status.data?.state === "STATUS_STATE_DEGRADED";
  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader
        title="Managed Providers"
        description="Platform-hosted provider instances — health, pins and data freshness"
        actions={
          <span className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[11.5px] font-semibold ${degraded ? "border-destructive/30 bg-destructive/10 text-destructive" : "border-success/30 bg-success/10 text-[var(--status-success)]"}`}>
            <span className={`inline-block size-1.5 rounded-full bg-current ${degraded ? "" : ""}`} />
            platform {degraded ? "degraded" : "healthy"}
          </span>
        }
      />
      <div className="grid gap-4 lg:grid-cols-2">
        {MANAGED_PROVIDERS.map((provider) => (
          <ProviderCardView key={provider.key} provider={provider} health={componentHealth(status.data?.components, provider.key)} />
        ))}
      </div>
      <p className="mt-4 text-[11.5px] text-muted-foreground">
        cAdvisor runs per node for container metrics. Health is probed per provider (2s budget; timeout counts as unhealthy) and
        aggregated into the platform state. Endpoints are config-defined (config file is the single source) and not exposed over
        the API.
      </p>
    </div>
  );
}

function ProviderCardView({ provider, health }: { provider: ManagedProviderCard; health: ReturnType<typeof componentHealth> }) {
  const probed = health != null;
  const healthy = health?.healthy === true;
  const badge = !probed ? (
    <StatusBadge tone="neutral">unverified</StatusBadge>
  ) : healthy ? (
    <StatusBadge tone="success">healthy</StatusBadge>
  ) : (
    <StatusBadge tone="danger" pulse>
      unhealthy
    </StatusBadge>
  );
  return (
    <section className="flex flex-col gap-3 rounded-xl border bg-card p-5">
      <div className="flex items-center gap-3">
        <span className="grid size-9 place-items-center rounded-lg bg-muted text-muted-foreground">
          <provider.icon className="size-4" />
        </span>
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-[15px] font-bold">{provider.name}</span>
            {badge}
          </div>
          <div className="text-[11.5px] text-muted-foreground">{provider.role}</div>
        </div>
        <div className="ml-auto text-right">
          <div className="font-mono text-[11.5px]">{provider.pinned}</div>
          <div className="text-[10.5px] text-muted-foreground">pinned (provider)</div>
        </div>
      </div>
      {!probed || healthy ? null : (
        <p className="rounded-md border border-destructive/30 bg-destructive/10 px-2.5 py-1.5 text-[11.5px] text-destructive">
          {health?.details || "health check failed"}
        </p>
      )}
      <div className="flex items-center gap-2 text-xs">
        <span className="w-32 flex-none text-muted-foreground">ingest freshness</span>
        {provider.freshness === "metrics-probe" ? (
          <MetricsFreshness />
        ) : provider.freshness === "needs-vl-proxy" ? (
          <span className="font-mono text-[11.5px] text-muted-foreground">n/a — needs VL query proxy (phase 3)</span>
        ) : (
          <span className="font-mono text-[11.5px] text-muted-foreground">n/a</span>
        )}
      </div>
      {provider.workbench ? (
        <div className="mt-1 flex gap-2 border-t pt-3">
          <Button size="sm" variant="outline" asChild>
            <a href={provider.workbench.href}>{provider.workbench.label}</a>
          </Button>
        </div>
      ) : null}
    </section>
  );
}

function MetricsFreshness() {
  const series = useMetricsSeries(METRICS_FRESHNESS_QUERY, "30m");
  const seconds = lastPointValue(series.data);
  const fresh = formatFreshness(seconds);
  const toneClass: Record<StatusTone, string> = {
    success: "text-emerald-600 dark:text-emerald-400",
    warning: "text-amber-600 dark:text-amber-400",
    danger: "text-destructive",
    info: "text-info",
    neutral: "text-muted-foreground",
  };
  return (
    <span className={`font-mono text-[11.5px] font-semibold ${toneClass[fresh.tone]}`}>{fresh.label}</span>
  );
}
