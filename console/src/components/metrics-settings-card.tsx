// metrics 设置卡（E6 W5-S3，D-W5-2 opt-in；SystemPage Metrics 页签）：
// 模式开关（on 部署三件套 / unset 移除——数据卷保留）+ 栈状态视图
//（三件部署态 + 「N/M nodes reporting」诚实口径）+ 常驻诚实标注
//（跨节点采集走节点地址 VPC/LAN 直连 + 采集面暴露口径——不谎报）。
//
// 锚点（只增）：metrics-status-card / metrics-mode-toggle。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity } from "lucide-react";

import {
  getMetricsStatus,
  setMetricsMode,
} from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import { EnvelopeAlertFrom } from "@/components/envelope-alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

// 诚实口径常驻文案（与 CLI `metrics status` note 同源——§6 挂账票修订后
// 跨节点采集 = VM 经节点 advertise 地址直连（VPC/LAN），不依赖 overlay
// 数据面；采集端口对节点全部接口开放 = 采集面是内网面，公网访问由节点
// 防火墙负责；节点缺席只剩「不 Ready」与「VPC 不可达」两种因由）。
const CROSS_NODE_NOTE =
  "Cross-node collection goes over direct node addresses (VPC/LAN), not the overlay data plane. " +
  "Collector ports listen on all node interfaces — the scrape face is an intranet face; public access is " +
  "expected to be blocked by the node firewall. A node below full count is not Ready or unreachable from the manager.";

export function MetricsSettingsCard() {
  const queryClient = useQueryClient();
  const status = useQuery({
    queryKey: ["metrics", "status"],
    queryFn: getMetricsStatus,
    refetchInterval: 10000,
  });

  const setMode = useMutation({
    mutationFn: (mode: "unset" | "on") => setMetricsMode(mode),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["metrics"] }),
  });

  const mode = status.data?.mode ?? "";
  const isOn = mode === "on";
  const components = status.data?.components ?? [];

  return (
    <Card data-testid="metrics-status-card">
      <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
        <Activity aria-hidden className="h-4 w-4 text-muted-foreground" />
        <CardTitle className="text-sm font-semibold">Metrics</CardTitle>
        <CardDescription className="ml-auto text-xs">
          {isOn
            ? `${status.data?.nodes_reporting ?? 0}/${status.data?.nodes_total ?? 0} nodes reporting`
            : "opt-in — nothing deployed by default"}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 pt-4">
        {status.isError ? (
          <EnvelopeAlertFrom envelope={errorEnvelopeFrom(status.error)} />
        ) : (
          <>
            <div className="flex flex-wrap items-center justify-between gap-3 text-sm">
              <div>
                <div className="font-medium">metrics.mode: {mode}</div>
                <div className="text-xs text-muted-foreground">
                  {status.data?.mode_set
                    ? `explicitly set`
                    : `default (unset)`}{" "}
                  · retention {status.data?.retention_days} days
                </div>
              </div>
              <div className="flex items-center gap-2" data-testid="metrics-mode-toggle">
                <Button
                  size="sm"
                  variant={isOn ? "outline" : "default"}
                  disabled={isOn || setMode.isPending}
                  onClick={() => setMode.mutate("on")}
                >
                  Enable
                </Button>
                <Button
                  size="sm"
                  variant={isOn ? "default" : "outline"}
                  disabled={!isOn || setMode.isPending}
                  onClick={() => setMode.mutate("unset")}
                >
                  Disable
                </Button>
              </div>
            </div>

            {setMode.isError ? (
              <EnvelopeAlertFrom envelope={errorEnvelopeFrom(setMode.error)} />
            ) : null}

            {components.length > 0 ? (
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                {components.map((c) => (
                  <div
                    key={c.name}
                    className="rounded-md border p-3"
                    data-testid="metrics-component"
                  >
                    <code className="text-xs">{c.name}</code>
                    <div
                      className={`text-xs ${
                        c.exists
                          ? "text-emerald-600 dark:text-emerald-400"
                          : "text-amber-600 dark:text-amber-400"
                      }`}
                    >
                      {c.exists ? "deployed" : "not deployed (converging or disabled)"}
                    </div>
                  </div>
                ))}
              </div>
            ) : null}

            <p className="text-xs text-muted-foreground">{CROSS_NODE_NOTE}</p>
            <p className="text-xs text-muted-foreground">
              Disabling removes the three managed services but keeps the data volume —
              history resumes from where it stopped when re-enabled. The query face is
              an operator tool: PromQL is passed through verbatim (no query sandbox).
            </p>
            <p className="text-xs text-muted-foreground">
              Known limitation: on Docker hosts running the containerd-snapshotter mode
              (29.x default), cAdvisor cannot read swarm labels — per-service grouping
              in app resource cards may be empty even while raw container series are
              collected.
            </p>
          </>
        )}
      </CardContent>
    </Card>
  );
}
