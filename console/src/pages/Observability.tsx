import { useMemo, useState } from "react";
import { PageShell, ErrorNote, LoadingNote, EmptyNote, Modal, Field, TextInput, Select, PrimaryButton, RowButton, DangerRowButton, MutationBanner, TableWrap, TableHead, useApiMutation, formatTime } from "../components/ui";
import { apiSend } from "../api/client";
import { useProjects, useApps, useMetricsSeries, useAlertRules, useAlertStates, useAlertingChannels, METRIC_RANGES, type AppEntry } from "../lib/catalog";
import type { components as telemetrySchemas } from "../api/telemetry";

type MetricSeries = telemetrySchemas["schemas"]["v1MetricSeries"];
type AlertRule = telemetrySchemas["schemas"]["v1AlertRule"];
type AlertState = telemetrySchemas["schemas"]["v1AlertState"];
type Channel = telemetrySchemas["schemas"]["v1NotificationChannel"];

// 可观测页（C1 对齐批）：Metrics（PromQL 时序图表——多序列为服务端契约）
// + Alerts（规则/现行评估态/通知通道）。动词面对齐 CLI（alerts rules /
// channels / metrics query）；写后失效即反映，轮询是兜底。

const METRIC_PRESETS = [
  {
    key: "cpu_cores",
    label: "CPU (cores)",
    build: (appLabel: string) => `rate(container_cpu_usage_seconds_total{container_label_fleetly_ns_app="${appLabel}"}[2m])`,
  },
  {
    key: "cpu_percent",
    label: "CPU (% of node)",
    build: (appLabel: string) =>
      `100 * rate(container_cpu_usage_seconds_total{container_label_fleetly_ns_app="${appLabel}"}[2m]) / on(node) machine_cpu_cores`,
  },
  {
    key: "memory",
    label: "Memory working set (bytes)",
    build: (appLabel: string) => `container_memory_working_set_bytes{container_label_fleetly_ns_app="${appLabel}"}`,
  },
] as const;

export function ObservabilityPage() {
  const [tab, setTab] = useState<"metrics" | "alerts">("metrics");
  return (
    <PageShell title="Observability" hint="metrics charts, alert rules and notification channels">
      <div className="flex gap-1">
        {(["metrics", "alerts"] as const).map((candidate) => (
          <button
            key={candidate}
            type="button"
            onClick={() => setTab(candidate)}
            className={
              candidate === tab
                ? "rounded-md bg-slate-800 px-3 py-1.5 text-sm font-medium text-slate-100"
                : "rounded-md px-3 py-1.5 text-sm text-slate-400 hover:bg-slate-900 hover:text-slate-200"
            }
          >
            {candidate}
          </button>
        ))}
      </div>
      {tab === "metrics" ? <MetricsTab /> : <AlertsTab />}
    </PageShell>
  );
}

// ---- metrics ----

function MetricsTab() {
  const projects = useProjects();
  const [projectId, setProjectId] = useState("");
  const [appId, setAppId] = useState("");
  const apps = useApps(projectId !== "" ? projectId : "");
  const [presetKey, setPresetKey] = useState<string>(METRIC_PRESETS[0].key);
  const [customQuery, setCustomQuery] = useState("");
  const [rangeKey, setRangeKey] = useState<string>("1h");

  const appLabel = useMemo(() => {
    const app = (apps.data ?? []).find((candidate: AppEntry) => candidate.id === appId);
    // cadvisor 归因标签经 sanitizeNamePart 小写化（F2.5 评估面同款归一）。
    return (app?.id ?? appId).toLowerCase();
  }, [apps.data, appId]);

  const query = useMemo(() => {
    if (presetKey === "custom") return customQuery.trim();
    if (appId === "") return "";
    return METRIC_PRESETS.find((preset) => preset.key === presetKey)?.build(appLabel) ?? "";
  }, [presetKey, customQuery, appId, appLabel]);

  const series = useMetricsSeries(query, rangeKey);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end gap-3">
        <Field label="Project">
          <Select value={projectId} onChange={(event) => { setProjectId(event.target.value); setAppId(""); }} className="w-44">
            <option value="">— pick a project —</option>
            {(projects.data ?? []).map((project) => (
              <option key={project.id} value={project.id}>{project.name}</option>
            ))}
          </Select>
        </Field>
        <Field label="App">
          <Select value={appId} onChange={(event) => setAppId(event.target.value)} className="w-44" disabled={projectId === ""}>
            <option value="">— pick an app —</option>
            {(apps.data ?? []).map((app) => (
              <option key={app.id} value={app.id}>{app.name}</option>
            ))}
          </Select>
        </Field>
        <Field label="Metric">
          <Select value={presetKey} onChange={(event) => setPresetKey(event.target.value)} className="w-56">
            {METRIC_PRESETS.map((preset) => (
              <option key={preset.key} value={preset.key}>{preset.label}</option>
            ))}
            <option value="custom">custom PromQL…</option>
          </Select>
        </Field>
        <Field label="Range">
          <Select value={rangeKey} onChange={(event) => setRangeKey(event.target.value)} className="w-24">
            {Object.keys(METRIC_RANGES).map((key) => (
              <option key={key} value={key}>{key}</option>
            ))}
          </Select>
        </Field>
      </div>
      {presetKey === "custom" ? (
        <Field label="PromQL" hint="passthrough to the managed VictoriaMetrics (query_range)">
          <TextInput value={customQuery} onChange={(event) => setCustomQuery(event.target.value)} placeholder='rate(container_cpu_usage_seconds_total[2m])' spellCheck={false} />
        </Field>
      ) : null}
      {series.isPending && query !== "" ? <LoadingNote label="querying metrics…" /> : null}
      {series.isError ? <ErrorNote error={series.error} hint="The query was rejected — check the PromQL expression." /> : null}
      {series.data != null ? (
        series.data.length === 0 ? (
          <EmptyNote label="No series matched — pick an app with running processes, or widen the range." />
        ) : (
          <SeriesChart seriesList={series.data} />
        )
      ) : null}
    </div>
  );
}

// SERIES_COLORS 是多序列配色环（尾循环——序列数超色板即从头复用）。
const SERIES_COLORS = ["#38bdf8", "#a78bfa", "#34d399", "#fbbf24", "#f87171", "#f472b6", "#4ade80", "#94a3b8"];

// seriesLegend 取序列的区分标签：容器名 → 节点 → 首个标签（cadvisor 序列
// 归因链 node/name 最稳定；其余标签族兜底）。
function seriesLegend(series: MetricSeries): string {
  const labels = series.labels ?? {};
  if (labels.name) return shortenLegend(labels.name);
  if (labels.node) return labels.node;
  const key = Object.keys(labels)[0];
  return key !== undefined ? `${key}=${labels[key]}` : "series";
}

// SeriesChart 是零依赖 SVG 多序列折线图：y 归一到全域 max（注记标注），
// x 取采样点时间域；序列数超色板即循环复用。
function SeriesChart({ seriesList }: { seriesList: MetricSeries[] }) {
  const width = 960;
  const height = 200;
  const pad = { left: 8, right: 8, top: 10, bottom: 10 };
  let max = 0;
  let minT = Number.POSITIVE_INFINITY;
  let maxT = Number.NEGATIVE_INFINITY;
  for (const series of seriesList) {
    for (const point of series.points ?? []) {
      if (point.value != null && point.value > max) max = point.value;
      if (point.time != null) {
        const t = Date.parse(point.time);
        if (!Number.isNaN(t)) {
          if (t < minT) minT = t;
          if (t > maxT) maxT = t;
        }
      }
    }
  }
  if (!Number.isFinite(minT) || !Number.isFinite(maxT) || maxT <= minT) {
    minT = 0;
    maxT = 1;
  }
  if (max <= 0) max = 1;
  const x = (t: number) => pad.left + ((t - minT) / (maxT - minT)) * (width - pad.left - pad.right);
  const y = (v: number) => height - pad.bottom - (v / max) * (height - pad.top - pad.bottom);
  return (
    <div className="flex flex-col gap-2">
      <svg viewBox={`0 0 ${width} ${height}`} className="w-full rounded-lg border border-slate-800 bg-slate-950" role="img" aria-label="metric series chart">
        {seriesList.map((series, index) => {
          const points = (series.points ?? [])
            .filter((point) => point.time != null && point.value != null)
            .map((point) => `${x(Date.parse(point.time ?? ""))},${y(point.value ?? 0)}`)
            .join(" ");
          if (points === "") return null;
          return <polyline key={index} points={points} fill="none" stroke={SERIES_COLORS[index % SERIES_COLORS.length]} strokeWidth="1.5" />;
        })}
      </svg>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-slate-500">
        <span>peak {formatValue(max)}</span>
        {seriesList.slice(0, 12).map((series, index) => (
          <span key={index} className="inline-flex items-center gap-1.5">
            <span className="inline-block h-1.5 w-3 rounded-sm" style={{ backgroundColor: SERIES_COLORS[index % SERIES_COLORS.length] }} />
            {seriesLegend(series)}
          </span>
        ))}
        {seriesList.length > 12 ? <span>… +{seriesList.length - 12} more</span> : null}
      </div>
    </div>
  );
}

// shortenLegend 是图例短名（swarm 容器全名带 project/app 前缀——中段
// 截断保 task id 尾段，走查 F2：全名图例可读性差）。
function shortenLegend(name: string): string {
  if (name.length <= 36) return name;
  return `${name.slice(0, 18)}…${name.slice(-12)}`;
}

// formatValue 是图表注记的量纲缩写（bytes/K/M/G 与工程计数兜底）。
function formatValue(value: number): string {
  if (Math.abs(value) >= 1_073_741_824) return `${(value / 1_073_741_824).toFixed(2)} GiB`;
  if (Math.abs(value) >= 1_048_576) return `${(value / 1_048_576).toFixed(2)} MiB`;
  if (Math.abs(value) >= 1024) return `${(value / 1024).toFixed(2)} KiB`;
  return value >= 100 ? value.toFixed(0) : value.toFixed(2);
}

// ---- alerts ----

// ALERT_METRICS 是规则指标值域（值域冻结，ADR-0041；与 CLI 文案同源）。
const ALERT_METRICS = [
  { key: "cpu_percent", label: "CPU (% of node)" },
  { key: "memory_working_set_bytes", label: "Memory working set (bytes)" },
] as const;

function AlertsTab() {
  const [subTab, setSubTab] = useState<"rules" | "channels">("rules");
  return (
    <div className="flex flex-col gap-4">
      <div className="flex gap-1">
        {(["rules", "channels"] as const).map((candidate) => (
          <button
            key={candidate}
            type="button"
            onClick={() => setSubTab(candidate)}
            className={
              candidate === subTab
                ? "rounded-md bg-slate-800 px-3 py-1.5 text-xs font-medium text-slate-100"
                : "rounded-md px-3 py-1.5 text-xs text-slate-400 hover:bg-slate-900 hover:text-slate-200"
            }
          >
            {candidate}
          </button>
        ))}
      </div>
      {subTab === "rules" ? <RulesTab /> : <ChannelsTab />}
    </div>
  );
}

function RulesTab() {
  const rules = useAlertRules();
  const states = useAlertStates();
  const [createOpen, setCreateOpen] = useState(false);

  const stateOf = new Map((states.data ?? []).map((state) => [state.rule_id ?? "", state]));
  const firing = (states.data ?? []).filter((state) => state.state === "firing");

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-slate-200">Alert rules</h2>
        <PrimaryButton onClick={() => setCreateOpen(true)}>New rule…</PrimaryButton>
      </div>
      {firing.length > 0 ? (
        <TableWrap>
          <table className="w-full text-left text-sm">
            <TableHead columns={["firing now", "metric", "observed", "since"]} />
            <tbody>
              {firing.map((state: AlertState) => (
                <tr key={state.rule_id ?? state.app_id} className="border-t border-slate-800">
                  <td className="px-3 py-2">
                    <span className="rounded bg-red-950/60 px-1.5 py-0.5 text-xs font-medium text-red-300">firing</span>
                    {state.system ? <span className="ml-2 text-xs text-slate-500">system</span> : null}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-slate-300">{state.metric}</td>
                  <td className="px-3 py-2 font-mono text-xs text-slate-300">{state.observed_value?.toFixed(2)}</td>
                  <td className="px-3 py-2 text-xs text-slate-500">{formatTime(state.state_since)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      ) : null}
      {rules.isPending ? <LoadingNote label="loading rules…" /> : null}
      {rules.isError ? <ErrorNote error={rules.error} /> : null}
      {rules.data != null ? (
        rules.data.length === 0 ? (
          <EmptyNote label="No alert rules — create one to get notified when an app crosses a threshold." />
        ) : (
          <TableWrap>
            <table className="w-full text-left text-sm">
              <TableHead columns={["app", "metric", "threshold", "for", "state", "since", ""]} />
              <tbody>
                {rules.data.map((rule: AlertRule) => (
                  <RuleRow key={rule.id} rule={rule} live={stateOf.get(rule.id ?? "")} />
                ))}
              </tbody>
            </table>
          </TableWrap>
        )
      ) : null}
      <CreateRuleModal open={createOpen} onClose={() => setCreateOpen(false)} />
    </div>
  );
}

// RuleRow 是行级组件（每行自持删除 mutation——path 在构造期绑定本行 id，
// 无动态路径的 stale-closure 面；Resources.tsx 行模式同款）。
function RuleRow({ rule, live }: { rule: AlertRule; live: AlertState | undefined }) {
  const remove = useApiMutation({
    path: `/v1/alerts/rules/${encodeURIComponent(rule.id ?? "")}`,
    method: "DELETE",
    invalidate: [["alerts"]],
  });
  return (
    <tr className="border-t border-slate-800">
      <td className="px-3 py-2 font-mono text-xs text-slate-300" title={rule.app_id}>{rule.app_id}</td>
      <td className="px-3 py-2 font-mono text-xs text-slate-300">{rule.metric}</td>
      <td className="px-3 py-2 font-mono text-xs text-slate-300">{rule.threshold}</td>
      <td className="px-3 py-2 font-mono text-xs text-slate-500">{rule.for_seconds ?? "0"}s</td>
      <td className="px-3 py-2">
        <span className={(live?.state ?? rule.state) === "firing" ? "rounded bg-red-950/60 px-1.5 py-0.5 text-xs font-medium text-red-300" : "rounded bg-slate-800 px-1.5 py-0.5 text-xs text-slate-400"}>
          {live?.state ?? rule.state ?? "ok"}
        </span>
      </td>
      <td className="px-3 py-2 text-xs text-slate-500">{formatTime(live?.state_since ?? rule.state_since)}</td>
      <td className="px-3 py-2 text-right">
        <DangerRowButton confirm={`Delete alert rule ${rule.id}?`} disabled={remove.isPending} onClick={() => void remove.mutate()}>
          delete
        </DangerRowButton>
        {remove.isError ? <ErrorNote error={remove.error} /> : null}
      </td>
    </tr>
  );
}

function CreateRuleModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const projects = useProjects();
  const [projectId, setProjectId] = useState("");
  const apps = useApps(projectId);
  const [appId, setAppId] = useState("");
  const [metric, setMetric] = useState<string>(ALERT_METRICS[0].key);
  const [threshold, setThreshold] = useState("");
  const [forSeconds, setForSeconds] = useState("0");
  const create = useApiMutation<{ rule?: AlertRule }>({
    path: "/v1/alerts/rules",
    method: "POST",
    body: () => ({ app_id: appId, metric, threshold: Number(threshold), for_seconds: forSeconds === "" ? "0" : forSeconds }),
    invalidate: [["alerts"]],
  });
  return (
    <Modal title="New alert rule" open={open} onClose={onClose}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          create.mutate(undefined, { onSuccess: onClose });
        }}
      >
        <Field label="Project">
          <Select value={projectId} onChange={(event) => { setProjectId(event.target.value); setAppId(""); }}>
            <option value="">— pick a project —</option>
            {(projects.data ?? []).map((project) => (
              <option key={project.id} value={project.id}>{project.name}</option>
            ))}
          </Select>
        </Field>
        <Field label="App">
          <Select value={appId} onChange={(event) => setAppId(event.target.value)} disabled={projectId === ""}>
            <option value="">— pick an app —</option>
            {(apps.data ?? []).map((app) => (
              <option key={app.id} value={app.id}>{app.name}</option>
            ))}
          </Select>
        </Field>
        <Field label="Metric" hint="aggregation is max across the app's containers (hottest replica)">
          <Select value={metric} onChange={(event) => setMetric(event.target.value)}>
            {ALERT_METRICS.map((candidate) => (
              <option key={candidate.key} value={candidate.key}>{candidate.label}</option>
            ))}
          </Select>
        </Field>
        <Field label="Threshold" hint="cpu rules are percent of node; memory rules are bytes">
          <TextInput type="number" step="any" min="0" value={threshold} onChange={(event) => setThreshold(event.target.value)} placeholder={metric === "cpu_percent" ? "90" : "1073741824"} required />
        </Field>
        <Field label="For (seconds)" hint="consecutive breach window before firing (0 = immediately)">
          <TextInput type="number" min="0" value={forSeconds} onChange={(event) => setForSeconds(event.target.value)} />
        </Field>
        <MutationBanner pending={create.isPending} error={create.error} success={null} />
        <PrimaryButton disabled={create.isPending || appId === "" || threshold === ""}>Create rule</PrimaryButton>
      </form>
    </Modal>
  );
}

function ChannelsTab() {
  const channels = useAlertingChannels();
  const [createOpen, setCreateOpen] = useState(false);
  const [testChannelId, setTestChannelId] = useState("");
  const [testResult, setTestResult] = useState<string | null>(null);
  const [testError, setTestError] = useState<unknown>(null);

  async function runTest(channelId: string) {
    setTestChannelId(channelId);
    setTestResult(null);
    setTestError(null);
    try {
      const res = await apiSend<{ delivered?: boolean; error?: string }>(`/v1/channels/${encodeURIComponent(channelId)}/test`, "POST", {});
      setTestResult(res.delivered ? "delivered" : `not delivered${res.error ? ` — ${res.error}` : ""}`);
    } catch (cause) {
      setTestError(cause);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-slate-200">Notification channels</h2>
        <PrimaryButton onClick={() => setCreateOpen(true)}>New channel…</PrimaryButton>
      </div>
      {channels.isPending ? <LoadingNote label="loading channels…" /> : null}
      {channels.isError ? <ErrorNote error={channels.error} /> : null}
      {channels.data != null ? (
        channels.data.length === 0 ? (
          <EmptyNote label="No notification channels — register a webhook or telegram channel so alerts reach someone." />
        ) : (
          <TableWrap>
            <table className="w-full text-left text-sm">
              <TableHead columns={["name", "kind", "enabled", "last failure", "created", ""]} />
              <tbody>
                {channels.data.map((channel: Channel) => (
                  <ChannelRow
                    key={channel.id}
                    channel={channel}
                    testState={testChannelId === channel.id ? { result: testResult, error: testError } : undefined}
                    onTest={() => void runTest(channel.id ?? "")}
                  />
                ))}
              </tbody>
            </table>
          </TableWrap>
        )
      ) : null}
      <CreateChannelModal open={createOpen} onClose={() => setCreateOpen(false)} />
    </div>
  );
}

// ChannelRow 是行级组件（删除 mutation 每行自持——同 RuleRow 模式）。
function ChannelRow({
  channel,
  testState,
  onTest,
}: {
  channel: Channel;
  testState?: { result: string | null; error: unknown };
  onTest: () => void;
}) {
  const remove = useApiMutation({
    path: `/v1/channels/${encodeURIComponent(channel.id ?? "")}`,
    method: "DELETE",
    invalidate: [["settings", "channels"]],
  });
  return (
    <tr className="border-t border-slate-800">
      <td className="px-3 py-2 text-xs text-slate-200">{channel.name}</td>
      <td className="px-3 py-2 font-mono text-xs text-slate-400">{channel.kind}</td>
      <td className="px-3 py-2 text-xs text-slate-400">{channel.enabled === false ? "disabled" : "yes"}</td>
      <td className="max-w-xs truncate px-3 py-2 text-xs text-amber-300/80" title={channel.last_failure}>{channel.last_failure || "—"}</td>
      <td className="px-3 py-2 text-xs text-slate-500">{formatTime(channel.created_at)}</td>
      <td className="whitespace-nowrap px-3 py-2 text-right">
        <RowButton onClick={onTest}>test</RowButton>{" "}
        <DangerRowButton confirm={`Delete channel ${channel.name}?`} disabled={remove.isPending} onClick={() => void remove.mutate()}>
          delete
        </DangerRowButton>
        {testState != null ? (
          <div className="mt-1 text-xs">
            {testState.result != null ? <span className="text-slate-400">{testState.result}</span> : null}
            {testState.error != null ? <ErrorNote error={testState.error} /> : null}
          </div>
        ) : null}
        {remove.isError ? <ErrorNote error={remove.error} /> : null}
      </td>
    </tr>
  );
}

function CreateChannelModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState("webhook");
  const [url, setUrl] = useState("");
  const [botToken, setBotToken] = useState("");
  const [chatId, setChatId] = useState("");
  const create = useApiMutation<{ channel?: Channel }>({
    path: "/v1/channels",
    method: "POST",
    body: () =>
      kind === "telegram"
        ? { name, kind, bot_token: botToken, chat_id: chatId }
        : { name, kind, url },
    invalidate: [["settings", "channels"]],
  });
  return (
    <Modal title="New notification channel" open={open} onClose={onClose}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          create.mutate(undefined, { onSuccess: onClose });
        }}
      >
        <Field label="Name" hint="unique channel name">
          <TextInput value={name} onChange={(event) => setName(event.target.value)} placeholder="ops-webhook" autoFocus required />
        </Field>
        <Field label="Kind">
          <Select value={kind} onChange={(event) => setKind(event.target.value)}>
            <option value="webhook">webhook</option>
            <option value="telegram">telegram</option>
          </Select>
        </Field>
        {kind === "telegram" ? (
          <>
            <Field label="Bot token" hint="write-only — stored in an age envelope, never echoed back">
              <TextInput type="password" value={botToken} onChange={(event) => setBotToken(event.target.value)} required />
            </Field>
            <Field label="Chat ID">
              <TextInput value={chatId} onChange={(event) => setChatId(event.target.value)} required />
            </Field>
          </>
        ) : (
          <Field label="Webhook URL" hint="write-only — stored in an age envelope, never echoed back">
            <TextInput type="url" value={url} onChange={(event) => setUrl(event.target.value)} placeholder="https://hooks.example.invalid/…" required />
          </Field>
        )}
        <MutationBanner pending={create.isPending} error={create.error} success={null} />
        <PrimaryButton disabled={create.isPending || name === ""}>Create channel</PrimaryButton>
      </form>
    </Modal>
  );
}
