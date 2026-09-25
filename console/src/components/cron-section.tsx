// Cron 区块（E5 Cron，架构 §4.3）：App 详情对定时任务服务的如实呈现——
// cron 服务是「按点触发的一次性 job」（compose 声明但非长驻），标注
// scheduled 而非长驻 running 态；运行台账（cron_runs）+ 手动触发入口
//（与到点触发同链路：重叠/节点不可用不报错，响应携带 skipped 与原因）。
//
// 锚点契约（只增）：cron-runs-list / cron-trigger-button / cron-run-status。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Clock } from "lucide-react";
import { useState } from "react";

import {
  getRevisionSpec,
  listCronRuns,
  listRevisions,
  triggerCronRun,
} from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import type { ErrorEnvelope } from "@/api/errors";
import type { CronRunView } from "@/api/types";
import { extractCronServices, type CronServiceView } from "@/lib/compose-cron";
import { StatusDot } from "@/components/status-dot";
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
import { timeAgo } from "@/lib/utils";
import { useIsPlatformAdmin } from "@/lib/context";

// cron run 状态 → 状态点色（对齐 state-badge.tsx 色板口径：succeeded 绿 /
// failed·timeout 红 / started 进行中蓝 / skipped 非失败终态灰）。
function runTone(status: string): "green" | "red" | "neutral-blue" | "gray" {
  switch (status) {
    case "succeeded":
      return "green";
    case "failed":
    case "timeout":
      return "red";
    case "started":
      return "neutral-blue";
    default:
      return "gray";
  }
}

function RunStatusCell({ run }: { run: CronRunView }) {
  return (
    <span
      data-testid="cron-run-status"
      data-status={run.status}
      className="inline-flex items-center gap-1.5 text-xs font-medium"
    >
      <StatusDot tone={runTone(run.status ?? "")} />
      {run.status || "unknown"}
    </span>
  );
}

/** 单个 cron 服务行：scheduled 标注 + expression/timezone/timeout + 触发。
 * platformReadonly（P0-3 双门）= 平台管理员资源面只读——手动触发链隐藏；
 * 本组件原本就无角色门（非平台管理员各角色见同一触发钮——零变化）。 */
function CronServiceRow({
  app,
  service,
  platformReadonly,
}: {
  app: string;
  service: CronServiceView;
  platformReadonly: boolean;
}) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const [result, setResult] = useState<string>("");
  const [error, setError] = useState<ErrorEnvelope | null>(null);

  const triggerMutation = useMutation({
    mutationFn: () => triggerCronRun(app, service.name),
    onSuccess: (resp) => {
      setError(null);
      const run = resp.run;
      if (run?.status === "skipped") {
        setResult(`skipped (${run.skip_reason || "overlap"})`);
      } else {
        setResult(`triggered (${run?.status ?? "started"})`);
      }
      setConfirming(false);
      void queryClient.invalidateQueries({ queryKey: ["cron-runs", app] });
    },
    onError: (err) => {
      setResult("");
      setError(errorEnvelopeFrom(err));
      setConfirming(false);
    },
  });

  return (
    <div
      className="space-y-2 rounded-md border p-3"
      data-testid="cron-service-row"
      data-service={service.name}
    >
      <div className="flex flex-wrap items-center gap-2">
        <code className="text-xs font-semibold">{service.name}</code>
        <span className="text-xs text-muted-foreground">scheduled job</span>
        <span className="ml-auto flex items-center gap-2">
          {result ? (
            <span
              className={`text-xs ${
                result.startsWith("skipped")
                  ? "text-amber-600 dark:text-amber-400"
                  : "text-emerald-600 dark:text-emerald-400"
              }`}
              data-testid="cron-trigger-result"
            >
              {result}
            </span>
          ) : null}
          {!platformReadonly ? (
            confirming ? (
              <>
                <span className="text-xs text-muted-foreground">
                  Run {service.name} now?
                </span>
                <Button
                  type="button"
                  size="sm"
                  data-testid="cron-trigger-button"
                  disabled={triggerMutation.isPending}
                  onClick={() => triggerMutation.mutate()}
                >
                  {triggerMutation.isPending ? "Triggering…" : "Confirm"}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setConfirming(false)}
                >
                  Cancel
                </Button>
              </>
            ) : (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => {
                  setResult("");
                  setError(null);
                  setConfirming(true);
                }}
              >
                Run now
              </Button>
            )
          ) : null}
        </span>
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span>
          expression <code className="text-foreground">{service.expression}</code>
        </span>
        <span>
          timezone <code className="text-foreground">{service.timezone || "UTC"}</code>
        </span>
        <span>
          timeout <code className="text-foreground">{service.timeout || "10m0s (default)"}</code>
        </span>
      </div>
      {error ? (
        <div className="text-xs text-red-600 dark:text-red-400">
          <code>{error.code}</code> {error.message}
          {error.suggestion ? ` — ${error.suggestion}` : ""}
        </div>
      ) : null}
    </div>
  );
}

export function CronSection({ app }: { app: string }) {
  // 平台管理员资源面只读（P0-3 双门）：手动触发链隐藏、卡内原位说明
  //（CLI 等价命令 fleetly cron trigger 齐备，文案如实指路）。
  const platformReadonly = useIsPlatformAdmin();
  // cron 服务清单：从最近 active revision 的归一化快照现读（与调度器同源）。
  const revisionsQuery = useQuery({
    queryKey: ["revisions", app],
    queryFn: () => listRevisions(app),
  });
  const active = (revisionsQuery.data?.revisions ?? []).find(
    (r) => r.status === "active",
  );
  const specQuery = useQuery({
    queryKey: ["revision-spec", app, active?.id],
    queryFn: () => getRevisionSpec(app, active!.id ?? ""),
    enabled: active !== undefined,
  });
  const cronServices = extractCronServices(specQuery.data?.compose);

  const runsQuery = useQuery({
    queryKey: ["cron-runs", app],
    queryFn: () => listCronRuns(app),
    enabled: cronServices !== null && cronServices.length > 0,
    refetchInterval: 10000,
  });

  if (cronServices === null) {
    // 快照损坏/形态漂移：诚实提示，不伪造空清单。
    return (
      <Card>
        <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
          <Clock aria-hidden className="h-4 w-4 text-muted-foreground" />
          <CardTitle className="text-sm font-semibold">Scheduled jobs (cron)</CardTitle>
        </CardHeader>
        <CardContent className="pt-4">
          <p className="text-xs text-muted-foreground">
            Revision spec is unreadable — cron services cannot be listed.
          </p>
        </CardContent>
      </Card>
    );
  }
  if (cronServices.length === 0) return null;

  const runs = runsQuery.data?.runs ?? [];

  return (
    <Card className="md:col-span-2">
      <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
        <Clock aria-hidden className="h-4 w-4 text-muted-foreground" />
        <CardTitle className="text-sm font-semibold">Scheduled jobs (cron)</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 pt-4">
        {platformReadonly ? (
          // P0-3：平台管理员只读——说明行。
          <p className="text-xs text-muted-foreground" data-testid="platform-readonly-note">
            Platform administrators have read-only access to resources
            (separation of duties). Trigger jobs from the CLI with a machine
            token (<code>fleetly cron trigger</code>), or ask a team owner for
            a member role.
          </p>
        ) : null}
        <div className="space-y-2">
          {cronServices.map((s) => (
            <CronServiceRow key={s.name} app={app} service={s} platformReadonly={platformReadonly} />
          ))}
          <p className="text-xs text-muted-foreground">
            Cron services are one-shot jobs fired on schedule — they do not run
            between fires and never count toward the running state.
          </p>
        </div>

        <div data-testid="cron-runs-list" className="rounded-md border">
          {runs.length === 0 ? (
            <p className="px-3 py-4 text-xs text-muted-foreground">
              No cron runs recorded yet (last 20 per schedule are kept).
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Status</TableHead>
                  <TableHead>Service</TableHead>
                  <TableHead>Scheduled</TableHead>
                  <TableHead>Started</TableHead>
                  <TableHead>Finished</TableHead>
                  <TableHead>Note</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {runs.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell>
                      <RunStatusCell run={r} />
                    </TableCell>
                    <TableCell className="font-mono text-xs">{r.service}</TableCell>
                    <TableCell
                      className="whitespace-nowrap text-xs text-muted-foreground"
                      title={r.scheduled_at}
                    >
                      {timeAgo(r.scheduled_at)}
                    </TableCell>
                    <TableCell
                      className="whitespace-nowrap text-xs text-muted-foreground"
                      title={r.started_at}
                    >
                      {r.started_at ? timeAgo(r.started_at) : "—"}
                    </TableCell>
                    <TableCell
                      className="whitespace-nowrap text-xs text-muted-foreground"
                      title={r.finished_at}
                    >
                      {r.finished_at ? timeAgo(r.finished_at) : "—"}
                    </TableCell>
                    <TableCell className="text-xs">
                      {r.skip_reason ? (
                        <span className="text-amber-600 dark:text-amber-400">
                          skipped: {r.skip_reason}
                        </span>
                      ) : r.error ? (
                        <span
                          className="max-w-[240px] truncate text-red-600 dark:text-red-400"
                          title={r.error}
                        >
                          {r.error}
                        </span>
                      ) : (
                        "—"
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
