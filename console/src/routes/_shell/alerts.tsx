import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { BellIcon, PlusIcon } from "lucide-react";
import { toast } from "sonner";
import type { components as telemetrySchemas } from "@/api/telemetry";
import { apiSend } from "@/api/client";
import { useAlertRules, useAlertStates, useAlertingChannels } from "@/lib/catalog";
import { CreateRuleDialog } from "@/features/alerts/create-rule-dialog";
import { CliEquivalent, ListPagination, ListToolbar, useClientPage, useListFilter } from "@/components/domain/list-toolbar";
import { CopyButton } from "@/components/domain/copy-button";
import { ErrorState } from "@/components/domain/error-state";
import { PageHeader } from "@/components/domain/page-header";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, alertTone } from "@/components/domain/status-badge";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { PageTabs } from "@/components/domain/page-tabs";

// 告警页（List 变体，UI v2 批 3）：firing 摘要条置顶 + rules/channels 双
// tab（URL search param）。旧 Observability 三层嵌套 tab 在此拆平。

type AlertRule = telemetrySchemas["schemas"]["v1AlertRule"];
type AlertState = telemetrySchemas["schemas"]["v1AlertState"];
type Channel = telemetrySchemas["schemas"]["v1NotificationChannel"];

const alertsSearch = z.object({ tab: z.enum(["rules", "channels"]).optional() });

export const Route = createFileRoute("/_shell/alerts")({
  validateSearch: alertsSearch,
  component: AlertsPage,
});

function AlertsPage() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const tab = search.tab ?? "rules";
  const rules = useAlertRules();
  const states = useAlertStates();
  const firing = (states.data ?? []).filter((state) => state.state === "firing");
  const [createOpen, setCreateOpen] = useState(false);

  return (
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader
        title="Alerts"
        description="Alert rules and notification channels — evaluated every 30s"
      />

      {firing.length > 0 ? (
        <Card className="mb-4 border-[color-mix(in_oklch,var(--status-danger)_40%,var(--border))] bg-[color-mix(in_oklch,var(--status-danger)_6%,var(--card))] px-4 py-3">
          {firing.map((state) => (
            <div key={state.rule_id ?? state.app_id} className="flex flex-wrap items-center gap-2.5 text-[13px]">
              <StatusBadge tone="danger" pulse>
                firing
              </StatusBadge>
              <b>{state.app_id?.slice(0, 10)}…</b>
              <span className="font-mono text-xs">{state.metric}</span>
              <span>
                observed <b className="font-mono">{state.observed_value?.toFixed(2)}</b>
              </span>
              {state.state_since ? <RelativeTime value={state.state_since} className="text-xs text-muted-foreground" /> : null}
              {state.rule_id ? <CopyButton value={state.rule_id} /> : null}
            </div>
          ))}
        </Card>
      ) : null}

      <PageTabs
        tabs={[
          { value: "rules", label: "Rules" },
          { value: "channels", label: "Notification channels" },
        ]}
        current={tab}
        onChange={(value) => void navigate({ search: { tab: value } })}
      />

      <Card className="overflow-hidden">
        {tab === "rules" ? (
          <RulesPanel rules={rules.data ?? []} states={states.data ?? []} loading={rules.isPending} error={rules.isError ? rules.error : null} onRetry={() => void rules.refetch()} onCreate={() => setCreateOpen(true)} />
        ) : (
          <ChannelsPanel />
        )}
      </Card>
      <CliEquivalent command={tab === "rules" ? "fleetly alerts rules list" : "fleetly channels list"} />

      <CreateRuleDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  );
}

function RulesPanel({
  rules,
  states,
  loading,
  error,
  onRetry,
  onCreate,
}: {
  rules: AlertRule[];
  states: AlertState[];
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  onCreate: () => void;
}) {
  const stateOf = new Map(states.map((state) => [state.rule_id ?? "", state]));
  const [deleting, setDeleting] = useState<AlertRule | null>(null);
  const [query, setQuery] = useState("");
  const filtered = useListFilter(rules, query, (rule: AlertRule) => [rule.app_id ?? "", rule.metric ?? ""]);
  const { page, pageCount, pageRows, setPage } = useClientPage(filtered);

  return (
    <>
      <div className="px-3 pt-3">
        <ListToolbar
          label="alert rules"
          value={query}
          onChange={setQuery}
          placeholder="Filter rules..."
          total={rules.length}
          shown={filtered.length}
          actions={
            <Button size="sm" onClick={onCreate}>
              <PlusIcon data-icon-start-inline />
              New rule
            </Button>
          }
        />
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>App</TableHead>
            <TableHead>Metric</TableHead>
            <TableHead>Threshold</TableHead>
            <TableHead>For</TableHead>
            <TableHead>State</TableHead>
            <TableHead>Since</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {loading ? (
            Array.from({ length: 3 }).map((_, index) => (
              <TableRow key={index}>
                {Array.from({ length: 7 }).map((_, cell) => (
                  <TableCell key={cell} className="text-muted-foreground">
                    …
                  </TableCell>
                ))}
              </TableRow>
            ))
          ) : error != null ? (
            <TableRow>
              <TableCell colSpan={7}>
                <ErrorState error={error} onRetry={onRetry} />
              </TableCell>
            </TableRow>
          ) : rules.length === 0 ? (
            <TableRow>
              <TableCell colSpan={7}>
                <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
                  <BellIcon className="size-4" />
                  No alert rules — create one to get notified when an app crosses a threshold.
                </div>
              </TableCell>
            </TableRow>
          ) : filtered.length === 0 ? (
            <TableRow>
              <TableCell colSpan={7} className="py-8 text-center text-sm text-muted-foreground">
                No rules match the filter.
              </TableCell>
            </TableRow>
          ) : (
            pageRows.map((rule) => {
              const live = stateOf.get(rule.id ?? "");
              return (
                <TableRow key={rule.id}>
                  <TableCell className="font-mono text-xs">{rule.app_id}</TableCell>
                  <TableCell className="font-mono text-xs">{rule.metric}</TableCell>
                  <TableCell className="font-mono text-xs">{rule.threshold}</TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">{rule.for_seconds ?? "0"}s</TableCell>
                  <TableCell>
                    <StatusBadge tone={alertTone(live?.state ?? rule.state)} pulse={(live?.state ?? rule.state) === "firing"}>
                      {live?.state ?? rule.state ?? "ok"}
                    </StatusBadge>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    <RelativeTime value={live?.state_since ?? rule.state_since} />
                  </TableCell>
                  <TableCell className="text-right">
                    <Button variant="outline" size="sm" onClick={() => setDeleting(rule)}>
                      Delete
                    </Button>
                  </TableCell>
                </TableRow>
              );
            })
          )}
        </TableBody>
      </Table>
      <ListPagination page={page} pageCount={pageCount} setPage={setPage} total={filtered.length} />

      <AlertDialog open={deleting != null} onOpenChange={(open) => !open && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete alert rule?</AlertDialogTitle>
            <AlertDialogDescription>
              {deleting?.app_id?.slice(0, 10)}… · <span className="font-mono">{deleting?.metric}</span> stops evaluating
              immediately. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction asChild>
              <DeleteRuleButton id={deleting?.id ?? ""} onDone={() => setDeleting(null)} />
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function DeleteRuleButton({ id, onDone }: { id: string; onDone: () => void }) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: () => apiSend(`/v1/alerts/rules/${encodeURIComponent(id)}`, "DELETE"),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["alerts"] });
      toast("Alert rule deleted");
      onDone();
    },
    onError: (cause) => toast.error(`Delete failed — ${String(cause)}`),
  });
  return (
    <Button variant="destructive" disabled={remove.isPending} onClick={() => void remove.mutateAsync().catch(() => undefined)}>
      Delete rule
    </Button>
  );
}

function ChannelsPanel() {
  const channels = useAlertingChannels();
  const [deleting, setDeleting] = useState<Channel | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const filtered = useListFilter(channels.data ?? [], query, (channel: Channel) => [channel.name ?? "", channel.kind ?? ""]);
  const { page, pageCount, pageRows, setPage } = useClientPage(filtered);

  function runTest(channelId: string) {
    setTesting(channelId);
    void apiSend<{ delivered?: boolean; error?: string }>(`/v1/channels/${encodeURIComponent(channelId)}/test`, "POST", {})
      .then((res) => toast(res.delivered ? "Test delivered" : `Not delivered${res.error ? ` — ${res.error}` : ""}`))
      .catch((cause) => toast.error(`Test failed — ${String(cause)}`))
      .finally(() => setTesting(null));
  }

  return (
    <>
      <div className="px-3 pt-3">
        <ListToolbar
          label="channels"
          value={query}
          onChange={setQuery}
          placeholder="Filter channels..."
          total={(channels.data ?? []).length}
          shown={filtered.length}
          actions={
            /* 渠道创建 Dialog 批 6 补齐（表单面迁移尾巴，不在本批谎称完成） */
            <Button size="sm" variant="outline" disabled title="Ships with the settings milestone">
              <PlusIcon data-icon-start-inline />
              New channel
            </Button>
          }
        />
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Kind</TableHead>
            <TableHead>Enabled</TableHead>
            <TableHead>Last failure</TableHead>
            <TableHead>Created</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {channels.isPending ? (
            <TableRow>
              <TableCell colSpan={6} className="py-6 text-center text-muted-foreground">
                Loading…
              </TableCell>
            </TableRow>
          ) : (channels.data ?? []).length === 0 ? (
            <TableRow>
              <TableCell colSpan={6} className="py-8 text-center text-sm text-muted-foreground">
                No notification channels — register a webhook or telegram channel so alerts reach someone.
              </TableCell>
            </TableRow>
          ) : filtered.length === 0 ? (
            <TableRow>
              <TableCell colSpan={6} className="py-8 text-center text-sm text-muted-foreground">
                No channels match the filter.
              </TableCell>
            </TableRow>
          ) : (
            pageRows.map((channel) => (
              <TableRow key={channel.id}>
                <TableCell className="text-[13px] font-medium">
                  {channel.name}
                  <CopyButton value={channel.id ?? ""} className="ml-1" />
                </TableCell>
                <TableCell className="font-mono text-xs">{channel.kind}</TableCell>
                <TableCell>
                  <StatusBadge tone={channel.enabled === false ? "warning" : "success"}>
                    {channel.enabled === false ? "no" : "yes"}
                  </StatusBadge>
                </TableCell>
                <TableCell className="max-w-56 truncate text-xs text-[var(--status-warning)]" title={channel.last_failure}>
                  {channel.last_failure || "—"}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  <RelativeTime value={channel.created_at} />
                </TableCell>
                <TableCell className="text-right">
                  <div className="flex justify-end gap-1.5">
                    <Button variant="outline" size="sm" disabled={testing === channel.id} onClick={() => runTest(channel.id ?? "")}>
                      {testing === channel.id ? "Testing…" : "Test"}
                    </Button>
                    <Button variant="outline" size="sm" onClick={() => setDeleting(channel)}>
                      Delete
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
      <ListPagination page={page} pageCount={pageCount} setPage={setPage} total={filtered.length} />

      <AlertDialog open={deleting != null} onOpenChange={(open) => !open && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete notification channel {deleting?.name}?</AlertDialogTitle>
            <AlertDialogDescription>Alert rules referencing it will stop delivering. This cannot be undone.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction asChild>
              <DeleteChannelButton id={deleting?.id ?? ""} onDone={() => setDeleting(null)} />
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function DeleteChannelButton({ id, onDone }: { id: string; onDone: () => void }) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: () => apiSend(`/v1/channels/${encodeURIComponent(id)}`, "DELETE"),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["settings", "channels"] });
      toast("Channel deleted");
      onDone();
    },
    onError: (cause) => toast.error(`Delete failed — ${String(cause)}`),
  });
  return (
    <Button variant="destructive" disabled={remove.isPending} onClick={() => void remove.mutateAsync().catch(() => undefined)}>
      Delete channel
    </Button>
  );
}
