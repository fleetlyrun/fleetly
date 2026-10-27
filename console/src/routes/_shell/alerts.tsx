import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { BellIcon, PlusIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { toast } from "sonner";
import { useCollection } from "@lynx-go/uikit-core";
import type { components as telemetrySchemas } from "@/api/telemetry";
import { apiSend } from "@/api/client";
import {
  alertRulesResource,
  alertStatesLookup,
  alertStatesResource,
  alertsSearch,
  channelsResource,
  type AlertRuleRow,
  type ChannelRow,
} from "@/kit/alerts";
import { CreateRuleDialog } from "@/features/alerts/create-rule-dialog";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { CopyButton } from "@/components/domain/copy-button";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { ModulePage } from "@/components/domain/module-page";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, alertTone } from "@/components/domain/status-badge";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage as FleetListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";

// 告警页（List 变体，UI v2 批 3 → 2026-10-26 批1 Wave2-A useCollection 化）：
// firing 摘要条置顶（lead 槽）+ rules/channels 双 tab。rules/channels 列表
// 读面走 kit 双资源（15s 轮询沿用旧拍，快筛/分页进 URL——validateSearch
// 合并形态收编 tab 键 + 集合键）；alert states 批4（2026-10-27）也收进 kit
//（lead 槽 firing 摘要 + 行内 state 覆盖列的前端 join 数据源，不是列表——
// 大页 lookup 读面不写 URL）。rules×states join 列、Test/Delete 动作与
// CreateRuleDialog 原样保留。

type AlertState = telemetrySchemas["schemas"]["v1AlertState"];

const alertsTabSearch = z.object({ tab: z.enum(["rules", "channels"]).optional() });

export const Route = createFileRoute("/_shell/alerts")({
  validateSearch: (input: Record<string, unknown>) => ({
    ...alertsTabSearch.parse(input),
    ...alertsSearch.toSearchSchema()["~standard"].validate(input).value,
  }),
  component: AlertsPage,
});

function AlertsPage() {
  const navigate = Route.useNavigate();
  const tab = Route.useSearch({ select: (search) => search.tab ?? "rules" });
  // 批4：states 读面迁 kit useCollection（无 scope 平台域；大页 lookup 不
  // 截断 firing 总览；15s 轮询同旧拍；join 逻辑不动）。
  const states = useCollection(alertStatesResource, {
    from: Route.id,
    search: alertStatesLookup,
    query: { refetchInterval: 15_000 },
  });
  const [createOpen, setCreateOpen] = useState(false);
  const firing = states.rows.filter((state) => state.state === "firing");

  return (
    <ModulePage
      title="Alerts"
      description="Alert rules and notification channels — evaluated every 30s"
      lead={
        firing.length > 0 ? (
          <Card className="mb-4 border-[color-mix(in_oklch,var(--status-danger)_40%,var(--border))] bg-[color-mix(in_oklch,var(--status-danger)_6%,var(--card))] px-4 py-3">
            {firing.map((state) => (
              <div key={state.rule_id ?? state.app_id} className="flex flex-wrap items-center gap-2.5 text-sm">
                <StatusBadge tone="danger" pulse>
                  firing
                </StatusBadge>
                <b>{state.app_id?.slice(0, 10)}…</b>
                <span className="font-mono text-xs">{state.metric}</span>
                <span>
                  observed <b className="font-mono">{state.observed_value?.toFixed(2)}</b>
                </span>
                {state.state_since ? <RelativeTime value={state.state_since} className="text-xs text-muted" /> : null}
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
      onTabChange={(value) => void navigate({ search: (prev) => ({ ...prev, tab: value }) })}
    >
      {tab === "rules" ? (
        <RulesPanel from={Route.id} states={[...states.rows]} onCreate={() => setCreateOpen(true)} />
      ) : (
        <ChannelsPanel from={Route.id} />
      )}

      <CreateRuleDialog open={createOpen} onOpenChange={setCreateOpen} />
    </ModulePage>
  );
}

// RulesPanel：规则表（行内 state/since 由 states 前端 join 覆盖——live 值
// 优先，缺席回退行内联 state）+ Delete。
function RulesPanel({ from, states, onCreate }: { from: string; states: AlertState[]; onCreate: () => void }) {
  const queryClient = useQueryClient();
  const list = useCollection(alertRulesResource, {
    from,
    search: alertsSearch,
    query: { refetchInterval: 15_000 },
  });
  const stateOf = new Map(states.map((state) => [state.rule_id ?? "", state]));
  const [deleting, setDeleting] = useState<AlertRuleRow | null>(null);

  // deleteRule 供 ConfirmDialog 的 onConfirm 消费（失败经 toast 披露，
  // 弹窗由 ConfirmDialog 落定关窗——全站统一契约）。
  function deleteRule(id: string) {
    return apiSend(`/v1/alerts/rules/${encodeURIComponent(id)}`, "DELETE").then(() => {
      void queryClient.invalidateQueries({ queryKey: ["alerts"] });
      toast("Alert rule deleted");
    });
  }

  const columns: LegacyColumnDef<AlertRuleRow, any>[] = [
    {
      accessorKey: "app_id",
      header: "App",
      meta: { sortField: "app_id" },
      cell: ({ row }) => <span className="font-mono text-xs">{row.original.app_id}</span>,
    },
    {
      accessorKey: "metric",
      header: "Metric",
      meta: { sortField: "metric" },
      cell: ({ row }) => <span className="font-mono text-xs">{row.original.metric}</span>,
    },
    { accessorKey: "threshold", header: "Threshold", cell: ({ row }) => <span className="font-mono text-xs">{row.original.threshold}</span> },
    {
      accessorKey: "for_seconds",
      header: "For",
      cell: ({ row }) => <span className="font-mono text-xs text-muted">{row.original.for_seconds ?? "0"}s</span>,
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
          <span className="text-xs text-muted">
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

  const filtered = list.query.predicate != null;
  return (
    <>
      <FleetListPage
        list={list}
        toolbar={
          <div className="flex flex-wrap items-center justify-between gap-2">
            <FilterBar list={list} schema={alertsSearch} textPlaceholder="Filter rules..." />
            <div className="py-2 pr-3">
              <Button size="sm" onClick={onCreate}>
                <PlusIcon data-icon-start-inline />
                New rule
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No rules match the filter" : "No alert rules"}
        emptyDescription={
          filtered
            ? "Adjust or clear the filter to see rows again."
            : "Create one to get notified when an app crosses a threshold."
        }
        emptyAction={
          filtered ? undefined : (
            <Button size="sm" variant="outline" onClick={onCreate}>
              <BellIcon className="size-3.5" /> New rule
            </Button>
          )
        }
        appendix={<CliEquivalent command="fleetly alerts rules list" />}
      >
        <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
      </FleetListPage>

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

function ChannelsPanel({ from }: { from: string }) {
  const queryClient = useQueryClient();
  const list = useCollection(channelsResource, {
    from,
    search: alertsSearch,
    query: { refetchInterval: 15_000 },
  });
  const [deleting, setDeleting] = useState<ChannelRow | null>(null);
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

  const columns: LegacyColumnDef<ChannelRow, any>[] = [
    {
      accessorKey: "name",
      header: "Name",
      meta: { sortField: "name" },
      cell: ({ row }) => (
        <span className="text-sm font-medium">
          {row.original.name}
          <CopyButton value={row.original.id ?? ""} className="ml-1" />
        </span>
      ),
    },
    {
      accessorKey: "kind",
      header: "Kind",
      meta: { sortField: "kind" },
      cell: ({ row }) => <span className="font-mono text-xs">{row.original.kind}</span>,
    },
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
        <span className="text-xs text-muted">
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

  const filtered = list.query.predicate != null;
  return (
    <>
      <FleetListPage
        list={list}
        toolbar={
          <div className="flex flex-wrap items-center justify-between gap-2">
            <FilterBar list={list} schema={alertsSearch} textPlaceholder="Filter channels..." />
            <div className="py-2 pr-3">
              {/* 渠道创建 Dialog 批 6 补齐（表单面迁移尾巴，不在本批谎称完成） */}
              <Button size="sm" variant="outline" disabled title="Ships with the settings milestone">
                <PlusIcon data-icon-start-inline />
                New channel
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No channels match the filter" : "No notification channels"}
        emptyDescription={
          filtered
            ? "Adjust or clear the filter to see rows again."
            : "Register a webhook or telegram channel so alerts reach someone."
        }
        appendix={<CliEquivalent command="fleetly channels list" />}
      >
        <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
      </FleetListPage>

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
