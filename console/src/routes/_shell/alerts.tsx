import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { BellIcon, PlusIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { toast } from "sonner";
import type { components as telemetrySchemas } from "@/api/telemetry";
import { apiSend } from "@/api/client";
import { useAlertRules, useAlertStates, useAlertingChannels } from "@/lib/catalog";
import { CreateRuleDialog } from "@/features/alerts/create-rule-dialog";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { ListCard } from "@/components/domain/list-page";
import { ModulePage } from "@/components/domain/module-page";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, alertTone } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";

// 告警页（List 变体，UI v2 批 3；模板化收编 ModulePage+ListCard）：
// firing 摘要条置顶（lead 槽）+ rules/channels 双 tab（URL search param
// 由路由 validateSearch 自带）。旧 Observability 三层嵌套 tab 在此拆平。

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
    <ModulePage
      title="Alerts"
      description="Alert rules and notification channels — evaluated every 30s"
      lead={
        firing.length > 0 ? (
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
        ) : null
      }
      tabs={[
        { value: "rules", label: "Rules" },
        { value: "channels", label: "Notification channels" },
      ]}
      current={tab}
      onTabChange={(value) => void navigate({ search: { tab: value } })}
    >
      {tab === "rules" ? (
        <RulesPanel
          rules={rules.data ?? []}
          states={states.data ?? []}
          loading={rules.isPending}
          error={rules.isError ? rules.error : null}
          onRetry={() => void rules.refetch()}
          onCreate={() => setCreateOpen(true)}
        />
      ) : (
        <ChannelsPanel />
      )}

      <CreateRuleDialog open={createOpen} onOpenChange={setCreateOpen} />
    </ModulePage>
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
  const queryClient = useQueryClient();
  const stateOf = new Map(states.map((state) => [state.rule_id ?? "", state]));
  const [deleting, setDeleting] = useState<AlertRule | null>(null);

  // deleteRule 供 ConfirmDialog 的 onConfirm 消费（失败经 toast 披露，
  // 弹窗由 ConfirmDialog 落定关窗——全站统一契约）。
  function deleteRule(id: string) {
    return apiSend(`/v1/alerts/rules/${encodeURIComponent(id)}`, "DELETE").then(() => {
      void queryClient.invalidateQueries({ queryKey: ["alerts"] });
      toast("Alert rule deleted");
    });
  }

  const columns: LegacyColumnDef<AlertRule, any>[] = [
    { accessorKey: "app_id", header: "App", cell: ({ row }) => <span className="font-mono text-xs">{row.original.app_id}</span> },
    { accessorKey: "metric", header: "Metric", cell: ({ row }) => <span className="font-mono text-xs">{row.original.metric}</span> },
    { accessorKey: "threshold", header: "Threshold", cell: ({ row }) => <span className="font-mono text-xs">{row.original.threshold}</span> },
    {
      accessorKey: "for_seconds",
      header: "For",
      cell: ({ row }) => <span className="font-mono text-xs text-muted-foreground">{row.original.for_seconds ?? "0"}s</span>,
    },
    {
      id: "state",
      header: "State",
      cell: ({ row }) => {
        const live = stateOf.get(row.original.id ?? "");
        return (
          <StatusBadge tone={alertTone(live?.state ?? row.original.state)} pulse={(live?.state ?? row.original.state) === "firing"}>
            {live?.state ?? row.original.state ?? "ok"}
          </StatusBadge>
        );
      },
    },
    {
      id: "since",
      header: "Since",
      cell: ({ row }) => {
        const live = stateOf.get(row.original.id ?? "");
        return (
          <span className="text-xs text-muted-foreground">
            <RelativeTime value={live?.state_since ?? row.original.state_since} />
          </span>
        );
      },
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => (
        <div className="text-right">
          <Button variant="outline" size="sm" onClick={() => setDeleting(row.original)}>
            Delete
          </Button>
        </div>
      ),
    },
  ];

  return (
    <>
      <ListCard
        label="alert rules"
        placeholder="Filter rules..."
        rows={rules}
        fieldsOf={(rule) => [rule.app_id ?? "", rule.metric ?? ""]}
        loading={loading}
        error={error}
        onRetry={onRetry}
        columns={columns}
        cli="fleetly alerts rules list"
        actions={
          <Button size="sm" onClick={onCreate}>
            <PlusIcon data-icon-start-inline />
            New rule
          </Button>
        }
        empty={
          <EmptyState
            icon={BellIcon}
            title="No alert rules"
            description="Create one to get notified when an app crosses a threshold."
            actionLabel="New rule"
            onAction={onCreate}
          />
        }
      />

      <ConfirmDialog
        open={deleting != null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title="Delete alert rule?"
        description={
          <>
            {deleting?.app_id?.slice(0, 10)}… · <span className="font-mono">{deleting?.metric}</span> stops evaluating immediately.
            This cannot be undone.
          </>
        }
        confirmLabel="Delete rule"
        onConfirm={() => deleteRule(deleting?.id ?? "")}
      />
    </>
  );
}

function ChannelsPanel() {
  const channels = useAlertingChannels();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<Channel | null>(null);
  const [testing, setTesting] = useState<string | null>(null);

  function deleteChannel(id: string) {
    return apiSend(`/v1/channels/${encodeURIComponent(id)}`, "DELETE").then(() => {
      void queryClient.invalidateQueries({ queryKey: ["settings", "channels"] });
      toast("Channel deleted");
    });
  }

  function runTest(channelId: string) {
    setTesting(channelId);
    void apiSend<{ delivered?: boolean; error?: string }>(`/v1/channels/${encodeURIComponent(channelId)}/test`, "POST", {})
      .then((res) => toast(res.delivered ? "Test delivered" : `Not delivered${res.error ? ` — ${res.error}` : ""}`))
      .catch((cause) => toast.error(`Test failed — ${String(cause)}`))
      .finally(() => setTesting(null));
  }

  const rows = channels.data ?? [];
  const columns: LegacyColumnDef<Channel, any>[] = [
    {
      accessorKey: "name",
      header: "Name",
      cell: ({ row }) => (
        <span className="text-[13px] font-medium">
          {row.original.name}
          <CopyButton value={row.original.id ?? ""} className="ml-1" />
        </span>
      ),
    },
    { accessorKey: "kind", header: "Kind", cell: ({ row }) => <span className="font-mono text-xs">{row.original.kind}</span> },
    {
      accessorKey: "enabled",
      header: "Enabled",
      cell: ({ row }) => (
        <StatusBadge tone={row.original.enabled === false ? "warning" : "success"}>
          {row.original.enabled === false ? "no" : "yes"}
        </StatusBadge>
      ),
    },
    {
      accessorKey: "last_failure",
      header: "Last failure",
      cell: ({ row }) => (
        <span className="block max-w-56 truncate text-xs text-[var(--status-warning)]" title={row.original.last_failure}>
          {row.original.last_failure || "—"}
        </span>
      ),
    },
    {
      id: "created",
      header: "Created",
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
          <RelativeTime value={row.original.created_at} />
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => (
        <div className="flex justify-end gap-1.5">
          <Button variant="outline" size="sm" disabled={testing === row.original.id} onClick={() => runTest(row.original.id ?? "")}>
            {testing === row.original.id ? "Testing…" : "Test"}
          </Button>
          <Button variant="outline" size="sm" onClick={() => setDeleting(row.original)}>
            Delete
          </Button>
        </div>
      ),
    },
  ];

  return (
    <>
      <ListCard
        label="channels"
        placeholder="Filter channels..."
        rows={rows}
        fieldsOf={(channel) => [channel.name ?? "", channel.kind ?? ""]}
        loading={channels.isPending}
        error={channels.isError ? channels.error : null}
        onRetry={() => void channels.refetch()}
        columns={columns}
        cli="fleetly channels list"
        actions={
          /* 渠道创建 Dialog 批 6 补齐（表单面迁移尾巴，不在本批谎称完成） */
          <Button size="sm" variant="outline" disabled title="Ships with the settings milestone">
            <PlusIcon data-icon-start-inline />
            New channel
          </Button>
        }
        empty={
          <EmptyState
            icon={BellIcon}
            title="No notification channels"
            description="Register a webhook or telegram channel so alerts reach someone."
          />
        }
      />

      <ConfirmDialog
        open={deleting != null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`Delete notification channel ${deleting?.name ?? ""}?`}
        description="Alert rules referencing it will stop delivering. This cannot be undone."
        confirmLabel="Delete channel"
        onConfirm={() => deleteChannel(deleting?.id ?? "")}
      />
    </>
  );
}
