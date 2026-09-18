// 概览页：基本信息 + 放置/卷概览（服务/副本拓扑的 v0.1 投影面——compose
// 服务明细在 revision spec，此处呈现平台观测面）。

import { useQuery } from "@tanstack/react-query";
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

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle className="text-sm font-medium text-muted-foreground">
            Application
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {app ? (
            <>
              <Field label="Derived state" value={<StateBadge state={app.derived_state} />} />
              <Field label="Lifecycle" value={<code>{app.lifecycle}</code>} />
              <Field label="Created" value={formatTime(app.created_at)} />
              <Field label="Updated" value={`${timeAgo(app.updated_at)}`} />
              <Field label="ID" value={<code className="text-xs">{app.id}</code>} />
            </>
          ) : (
            <p className="text-sm text-muted-foreground">Loading…</p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-sm font-medium text-muted-foreground">
            Placement & volumes
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {placementQuery.data?.placement ? (
            <div className="space-y-3">
              <Field
                label="Binding state"
                value={
                  <StateBadge state={placementQuery.data.placement.state} />
                }
              />
              <Field
                label="Node"
                value={
                  <code className="text-xs">
                    {placementQuery.data.placement.label_ref ||
                      placementQuery.data.placement.platform_node_id}
                  </code>
                }
              />
              {placementQuery.data.placement.reason ? (
                <Field
                  label="Reason"
                  value={placementQuery.data.placement.reason}
                />
              ) : null}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
              No placement binding (stateful placement not declared).
            </p>
          )}
          {placementQuery.data?.volumes?.length ? (
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
                {placementQuery.data.volumes.map((v) => (
                  <TableRow key={v.key}>
                    <TableCell className="font-mono text-xs">{v.key}</TableCell>
                    <TableCell>{v.kind}</TableCell>
                    <TableCell className="font-mono text-xs">{v.mount_path}</TableCell>
                    <TableCell>{v.status}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}
