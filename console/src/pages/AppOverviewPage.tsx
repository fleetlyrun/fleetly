// 概览页：基本信息 + 服务清单（cron 服务标注 scheduled——compose 声明但
// 非长驻，不冒充长驻态）+ 放置/卷概览（服务拓扑明细在 revision spec）+
// cron 区块（运行台账 + 手动触发）。三/四卡片分区：Application /
// Placement / Services / Volumes / Scheduled jobs。

import { useQuery } from "@tanstack/react-query";
import { Boxes, Layers, MapPin, PackageOpen } from "lucide-react";
import { useParams } from "react-router-dom";

import {
  getApp,
  getPlacement,
  getRevisionSpec,
  listRevisions,
} from "@/api/endpoints";
import { formatTime, timeAgo } from "@/lib/utils";
import { CronSection } from "@/components/cron-section";
import { StatusDot } from "@/components/status-dot";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { StateBadge } from "@/components/state-badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { extractServiceNames } from "@/lib/compose-cron";

function Field({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-right font-medium">{value}</span>
    </div>
  );
}

export function AppOverviewPage() {
  const { name = "" } = useParams();
  const appQuery = useQuery({
    queryKey: ["app", name],
    queryFn: () => getApp(name),
    refetchInterval: 5000,
  });
  const placementQuery = useQuery({
    queryKey: ["placement", name],
    queryFn: () => getPlacement(name),
  });

  // 服务清单：最近 active revision 的归一化快照（与 cron 区块同源同查询）。
  const revisionsQuery = useQuery({
    queryKey: ["revisions", name],
    queryFn: () => listRevisions(name),
  });
  const active = (revisionsQuery.data?.revisions ?? []).find(
    (r) => r.status === "active",
  );
  const specQuery = useQuery({
    queryKey: ["revision-spec", name, active?.id],
    queryFn: () => getRevisionSpec(name, active!.id ?? ""),
    enabled: active !== undefined,
  });
  const services = extractServiceNames(specQuery.data?.compose);
  const hasCron = services !== null && services.some((s) => s.isCron);

  const app = appQuery.data;
  const placement = placementQuery.data?.placement;
  const volumes = placementQuery.data?.volumes ?? [];

  return (
    <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
      <Card>
        <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
          <Layers aria-hidden className="h-4 w-4 text-muted-foreground" />
          <CardTitle className="text-sm font-semibold">Application</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3 pt-4">
          {app ? (
            <>
              <Field label="Derived state" value={<StateBadge state={app.derived_state ?? ""} />} />
              <Field label="Lifecycle" value={<code>{app.lifecycle}</code>} />
              <Field label="Created" value={<span title={app.created_at}>{formatTime(app.created_at)}</span>} />
              <Field label="Updated" value={<span title={app.updated_at}>{timeAgo(app.updated_at)}</span>} />
              <Field label="ID" value={<code className="text-xs">{app.id}</code>} />
            </>
          ) : (
            <p className="text-sm text-muted-foreground">Loading…</p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
          <MapPin aria-hidden className="h-4 w-4 text-muted-foreground" />
          <CardTitle className="text-sm font-semibold">Placement</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3 pt-4">
          {placement ? (
            <>
              <Field
                label="Binding state"
                value={<StateBadge state={placement.state ?? ""} />}
              />
              <Field
                label="Node"
                value={
                  <code className="text-xs">
                    {placement.label_ref || placement.platform_node_id}
                  </code>
                }
              />
              {placement.reason ? (
                <Field label="Reason" value={placement.reason} />
              ) : null}
            </>
          ) : (
            <p className="text-sm text-muted-foreground">
              No placement binding (stateful placement not declared).
            </p>
          )}
        </CardContent>
      </Card>

      <Card className="md:col-span-2">
        <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
          <Boxes aria-hidden className="h-4 w-4 text-muted-foreground" />
          <CardTitle className="text-sm font-semibold">Services</CardTitle>
        </CardHeader>
        <CardContent className="pt-4" data-testid="services-list">
          {services === null ? (
            <p className="text-xs text-muted-foreground">
              Revision spec is unreadable — services cannot be listed.
            </p>
          ) : services.length === 0 ? (
            <p className="text-xs text-muted-foreground">
              No revision deployed yet.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Service</TableHead>
                  <TableHead>Mode</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {services.map((s) => (
                  <TableRow key={s.name}>
                    <TableCell className="font-mono text-xs">{s.name}</TableCell>
                    <TableCell>
                      {s.isCron ? (
                        // cron 服务 = 按点触发的一次性 job：如实标注 scheduled，
                        // 不冒充长驻 running 态（架构 §4.3）。
                        <span
                          data-testid="service-scheduled-badge"
                          className="inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-semibold"
                        >
                          <StatusDot tone="neutral-blue" />
                          scheduled
                        </span>
                      ) : (
                        <span className="text-xs text-muted-foreground">long-running</span>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {volumes.length > 0 ? (
        <Card className="md:col-span-2">
          <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
            <PackageOpen aria-hidden className="h-4 w-4 text-muted-foreground" />
            <CardTitle className="text-sm font-semibold">Volumes</CardTitle>
          </CardHeader>
          <CardContent className="pt-4">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Key</TableHead>
                  <TableHead>Kind</TableHead>
                  <TableHead>Mount</TableHead>
                  <TableHead>Status</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {volumes.map((v) => (
                  <TableRow key={v.key}>
                    <TableCell className="font-mono text-xs">{v.key}</TableCell>
                    <TableCell>{v.kind}</TableCell>
                    <TableCell className="font-mono text-xs">{v.mount_path}</TableCell>
                    <TableCell>{v.status}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      ) : null}

      {hasCron ? <CronSection app={name} /> : null}
    </div>
  );
}
