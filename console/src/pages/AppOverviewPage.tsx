// 概览页：基本信息 + 放置/卷概览（服务/副本拓扑的 v0.1 投影面——compose
// 服务明细在 revision spec，此处呈现平台观测面）。三卡片分区：Application /
// Placement / Volumes。

import { useQuery } from "@tanstack/react-query";
import { Layers, MapPin, PackageOpen } from "lucide-react";
import { useParams } from "react-router-dom";

import { getApp, getPlacement } from "@/api/endpoints";
import { formatTime, timeAgo } from "@/lib/utils";
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
    </div>
  );
}
