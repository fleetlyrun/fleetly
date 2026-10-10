import { ActivityIcon, GlobeIcon, LayersIcon, ScrollTextIcon } from "lucide-react";
import {
  DISK_WATERMARK_QUERY,
  METRICS_FRESHNESS_QUERY,
  formatFreshness,
  formatWatermark,
  lastPointValue,
} from "@/features/fleet/freshness";
import { componentHealth, useSystemStatus } from "@/features/fleet/use-system-status";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { apiSend } from "@/api/client";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { useMetricsSeries } from "@/lib/catalog";
import { PageHeader } from "@/components/domain/page-header";
import { StatusBadge, type StatusTone } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";

// Components 页（IA v3 T6，ADR-0058 + ADR-0059 复裁）：受管组件的排障
// 驾驶舱。页名 ADR-0058 定 Managed Providers、ADR-0059 复裁 Components
// （原型命名直觉胜出；route 保持 /providers）。实名露出为 2026-10-09
// 拍板（推翻 "managed proxy" 泛称）。
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
  const diskSeries = useMetricsSeries(DISK_WATERMARK_QUERY, "30m");
  const diskUsage = formatWatermark(lastPointValue(diskSeries.data));
  const degraded = status.data?.state === "STATUS_STATE_DEGRADED";
  return (
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader
        title="Components"
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
          <ProviderCardView
            key={provider.key}
            provider={provider}
            health={componentHealth(status.data?.components, provider.key)}
            diskUsage={diskUsage}
            onRestarted={() => void status.refetch()}
          />
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

// NodeDiskWatermark 节点磁盘水位（IA v3 二期⑤b，§5.2 "吃得下吗"层）：
// cadvisor fs 指标（usage/limit 比值，最满文件系统口径）逐节点一行。
function ProviderCardView({
  provider,
  health,
  diskUsage,
  onRestarted,
}: {
  provider: ManagedProviderCard;
  health: ReturnType<typeof componentHealth>;
  diskUsage: ReturnType<typeof formatWatermark>;
  onRestarted: () => void;
}) {
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
      {/* disk usage（原型卡内行形态，IA v3 对齐批）：数据卷所在宿主盘水位
          ——单机拓扑四卡同值是事实；多节点分化后此行接 per-provider 载体
          节点锚（GetStatus 载体节点信息挂账）。 */}
      <div className="flex items-center gap-2 text-xs">
        <span className="w-32 flex-none text-muted-foreground">disk usage</span>
        <span className="hidden h-1.5 w-40 overflow-hidden rounded-full bg-muted sm:block">
          <span
            className={`block h-full rounded-full ${
              diskUsage.tone === "danger" ? "bg-destructive" : diskUsage.tone === "warning" ? "bg-amber-500" : "bg-emerald-500"
            }`}
            style={{ width: `${Math.min(100, Math.round(diskUsage.ratio * 100))}%` }}
          />
        </span>
        <span className={`font-mono text-[11.5px] font-semibold ${diskUsage.tone === "danger" ? "text-destructive" : ""}`}>
          {diskUsage.label}
        </span>
        <span className="text-[11px] text-muted-foreground">root fs</span>
      </div>
      <div className="mt-1 flex gap-2 border-t pt-3">
        {provider.workbench ? (
          <Button size="sm" variant="outline" asChild>
            <a href={provider.workbench.href}>{provider.workbench.label}</a>
          </Button>
        ) : null}
        <RestartButton name={provider.name} onRestarted={onRestarted} />
      </div>
    </section>
  );
}

// RestartButton（IA v3 二期④）：载体重启（Runtime 重启子面强制重排）——
// 破坏性动作走 AlertDialog 确认（守卫执法：禁 window.confirm）；成功后
// 刷新健康面（滚动重排期间 unhealthy 是预期中间态）。
function RestartButton({ name, onRestarted }: { name: string; onRestarted: () => void }) {
  const queryClient = useQueryClient();
  const restart = useMutation({
    mutationFn: async () => apiSend<{ restarted?: number }>(`/v1/system/components/${encodeURIComponent(name)}/restart`, "POST", {}),
    onSuccess: (resp) => {
      toast(`Restart requested — ${resp?.restarted ?? 1} carrier(s) rescheduling`);
      void queryClient.invalidateQueries({ queryKey: ["system", "status"] });
      onRestarted();
    },
    onError: (cause) => toast.error(cause instanceof Error ? cause.message : String(cause)),
  });
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button size="sm" variant="outline" disabled={restart.isPending}>
          {restart.isPending ? "Restarting…" : "Restart…"}
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Restart {name}?</AlertDialogTitle>
          <AlertDialogDescription>
            The carrier is force-rescheduled (tasks replaced with a rolling restart). Brief downtime on this component's
            face is expected while the replacement becomes healthy.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={restart.isPending}
            onClick={(event) => {
              event.preventDefault();
              restart.mutate();
            }}
          >
            {restart.isPending ? "Restarting…" : "Restart"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
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
