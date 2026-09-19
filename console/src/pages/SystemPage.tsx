// 系统页（只读）：组件健康（system/status）、节点列表（system/nodes）、
// 入口状态（system/ingress）。

import { useQuery } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";

import { getIngressStatus, getSystemStatus, listNodes } from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import {
  EnvelopeAlert,
  EnvelopeAlertFrom,
} from "@/components/envelope-alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatTime } from "@/lib/utils";

function HealthDot({ ok }: { ok: boolean }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-xs font-medium">
      <span
        aria-hidden
        className={`h-2 w-2 rounded-full ${ok ? "bg-emerald-500" : "bg-red-500"}`}
      />
      {ok ? "healthy" : "unhealthy"}
    </span>
  );
}

export function SystemPage() {
  const status = useQuery({
    queryKey: ["system", "status"],
    queryFn: getSystemStatus,
    refetchInterval: 10000,
  });
  const nodes = useQuery({
    queryKey: ["system", "nodes"],
    queryFn: listNodes,
    refetchInterval: 15000,
  });
  const ingress = useQuery({
    queryKey: ["system", "ingress"],
    queryFn: getIngressStatus,
    refetchInterval: 15000,
  });

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">System</h1>
        <Button variant="outline" size="sm" onClick={() => {
          void status.refetch();
          void nodes.refetch();
          void ingress.refetch();
        }}>
          <RefreshCw aria-hidden className="h-3.5 w-3.5" />
          Refresh
        </Button>
      </div>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-sm font-medium text-muted-foreground">
            Control plane · {status.data?.service ?? "fleetlyd"}
            {status.data?.version ? ` v${status.data.version}` : ""}
          </CardTitle>
        </CardHeader>
        <CardContent>
          {status.isError ? (
            <EnvelopeAlertFrom envelope={errorEnvelopeFrom(status.error)} />
          ) : (
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
              {(status.data?.components ?? []).map((c) => (
                <div
                  key={c.name}
                  className="flex items-center justify-between rounded-md border p-3"
                  data-testid="component-health"
                >
                  <code className="text-xs">{c.name}</code>
                  <span className="flex items-center gap-2">
                    {c.error ? (
                      <span
                        className="max-w-[180px] truncate text-xs text-red-700"
                        title={c.error}
                      >
                        {c.error}
                      </span>
                    ) : null}
                    <HealthDot ok={c.ok ?? false} />
                  </span>
                </div>
              ))}
              {status.isPending ? (
                <p className="text-sm text-muted-foreground">Loading…</p>
              ) : null}
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-sm font-medium text-muted-foreground">
            Nodes
          </CardTitle>
        </CardHeader>
        <CardContent>
          {nodes.isError ? (
            <EnvelopeAlert
              code={errorEnvelopeFrom(nodes.error).code}
              message={errorEnvelopeFrom(nodes.error).message}
              suggestion={errorEnvelopeFrom(nodes.error).suggestion}
            />
          ) : (nodes.data?.nodes ?? []).length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No nodes observed yet (Docker Swarm idle or unreachable).
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Hostname</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead>Availability</TableHead>
                  <TableHead>Manager</TableHead>
                  <TableHead>Platform ID</TableHead>
                  <TableHead>Observed</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(nodes.data?.nodes ?? []).map((n) => (
                  <TableRow key={n.swarm_node_id}>
                    <TableCell>{n.hostname}</TableCell>
                    <TableCell className="text-xs">
                      {n.state}
                      {n.stale ? (
                        <span className="ml-1 text-amber-700">(stale)</span>
                      ) : null}
                    </TableCell>
                    <TableCell className="text-xs">{n.availability}</TableCell>
                    <TableCell className="text-xs">
                      {n.is_manager ? "yes" : "no"}
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {n.platform_id || "—"}
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                      {formatTime(n.observed_at)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-sm font-medium text-muted-foreground">
            Ingress
          </CardTitle>
        </CardHeader>
        <CardContent>
          {ingress.isError ? (
            <EnvelopeAlert
              code={errorEnvelopeFrom(ingress.error).code}
              message={errorEnvelopeFrom(ingress.error).message}
              suggestion={errorEnvelopeFrom(ingress.error).suggestion}
            />
          ) : !ingress.data ? (
            <p className="text-sm text-muted-foreground">Loading…</p>
          ) : (
            <div className="space-y-3 text-sm">
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                <div className="rounded-md border p-3">
                  <div className="text-xs text-muted-foreground">Traefik</div>
                  {ingress.data.traefik?.exists ? (
                    <>
                      <code className="text-xs">{ingress.data.traefik.image}</code>
                      <div className="text-xs text-muted-foreground">
                        {ingress.data.traefik.static_args} static args
                      </div>
                    </>
                  ) : (
                    <span className="text-xs text-red-700">
                      {ingress.data.traefik?.error || "not present"}
                    </span>
                  )}
                </div>
                <div className="rounded-md border p-3">
                  <div className="text-xs text-muted-foreground">Config endpoint</div>
                  <code className="text-xs">{ingress.data.config_addr}</code>
                  <div className="text-xs">
                    healthz: {ingress.data.healthz || "—"}
                  </div>
                  <div className="text-xs">auth: {ingress.data.auth || "—"}</div>
                </div>
              </div>
              {(ingress.data.certificates ?? []).length > 0 ? (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>App</TableHead>
                      <TableHead>Domain</TableHead>
                      <TableHead>Expires</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {(ingress.data.certificates ?? []).map((c) => (
                      <TableRow key={`${c.app}:${c.domain}`}>
                        <TableCell>{c.app}</TableCell>
                        <TableCell className="font-mono text-xs">{c.domain}</TableCell>
                        <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                          {formatTime(c.cert_not_after)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              ) : (
                <p className="text-xs text-muted-foreground">
                  No certificates issued yet.
                </p>
              )}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
