// 告警设置卡（B 线 W5-S2，D-V3W5-1；SystemPage Alerts 页签）：alerts.mode
// 开关（**前置门 metrics.mode=on**——metrics 未开时禁用态提示）+ 栈状态
// 视图（vmalert 部署态 + 规则数）+ 告警规则管理（expr 编辑 + Test 即时
// 求值 + 通道绑定勾选 + for 时长——设计 §2.3 Console 面原文；规则编辑
// backlog #4-④：updateAlertRule 接 UI，复用创建表单形态预填）。
//
// 锚点（只增）：alerting-status-card / alerts-mode-toggle / alerting-rule-row
// / alerting-rule-form / alerting-test-result / alert-rule-edit。

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BellRing } from "lucide-react";

import {
  createAlertRule,
  deleteAlertRule,
  getAlertsStatus,
  getMetricsStatus,
  listAlertRules,
  listWebhookEndpoints,
  setAlertsMode,
  testAlertRule,
  updateAlertRule,
} from "@/api/endpoints";
import type { AlertRuleView } from "@/api/endpoints";
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
import { Input } from "@/components/ui/input";
import { useIsPlatformAdmin } from "@/lib/context";

/** 非平台管理员的写面说明（2026-09-25 走查——此前 viewer/developer 见到
 * enabled 的模式钮/规则 CRUD 假按钮）：状态读面保留，写面原位说明。 */
const PLATFORM_ADMIN_NOTE = "Platform administrator required.";

// TestAlertRule 求值结果的样本投影（即时校验面的展示形态）。
function TestResult({ expr }: { expr: string }) {
  const [result, setResult] = useState<string | null>(null);
  const test = useMutation({
    mutationFn: () => testAlertRule(expr),
    onSuccess: (resp) => {
      const series = resp.series ?? [];
      if (series.length === 0) {
        setResult("OK — expression valid, 0 matching series right now");
        return;
      }
      const first = series[0];
      const labels = Object.entries(first.metric ?? {})
        .map(([k, v]) => `${k}=${v}`)
        .join(", ");
      const value = first.points?.[0]?.v ?? "";
      setResult(`OK — ${series.length} series; first: {${labels}} value=${value}`);
    },
  });  return (
    <div className="space-y-1">
      <Button
        type="button"
        size="sm"
        variant="outline"
        disabled={expr.trim() === "" || test.isPending}
        onClick={() => test.mutate()}
        data-testid="alerting-test-button"
      >
        {test.isPending ? "Evaluating…" : "Test expression"}
      </Button>
      {test.isError ? (
        <EnvelopeAlertFrom envelope={errorEnvelopeFrom(test.error)} />
      ) : null}
      {result ? (
        <p className="text-xs text-muted-foreground" data-testid="alerting-test-result">
          {result}
        </p>
      ) : null}
    </div>
  );
}

// 规则表单：name/expr/for/severity + 通道绑定勾选（channels 缺省全端点）。
// rule 传入 = 编辑态（backlog #4-④）：预填规则行、提交走 PUT 部分更新；
// 缺省 = 创建态（POST）。角色门沿该卡既有创建/删除形态（无前端门，服务端
// requirePlatformWriteFace 硬门，403 信封照实展示）。
function RuleForm({ rule, onClose }: { rule?: AlertRuleView; onClose: () => void }) {
  const queryClient = useQueryClient();
  // 编辑态预填（view 投影 → 表单值）：for_duration_seconds 是 int64 的
  // JSON 形态（protojson 出 string，fixture 可能是 number——Number 归一）；
  // severity 取 labels.severity；channels 直投影。
  const [name, setName] = useState(rule?.name ?? "");
  const [expr, setExpr] = useState(rule?.expr ?? "");
  const [forSeconds, setForSeconds] = useState(String(Number(rule?.for_duration_seconds ?? 0)));
  const [severity, setSeverity] = useState(rule?.labels?.severity ?? "");
  const [channelIds, setChannelIds] = useState<string[]>(rule?.channels ?? []);

  const endpoints = useQuery({
    queryKey: ["notifications", "endpoints"],
    queryFn: listWebhookEndpoints,
  });

  const save = useMutation({
    mutationFn: () => {
      const labels: Record<string, string> = severity === "" ? {} : { severity };
      const payload = {
        name,
        expr,
        for_duration_seconds: Number(forSeconds) || 0,
        labels,
        channels: channelIds,
      };
      // PUT 为部分更新语义（alerting.proto UpdateAlertRuleRequest）：labels
      // 非 null 整体替换、channels 非空整体替换——载荷字段与创建对齐。
      return rule
        ? updateAlertRule(rule.id ?? "", payload)
        : createAlertRule(payload);
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["alerting"] });
      onClose();
    },
  });

  return (
    <form
      className="space-y-3 rounded-md border p-3"
      data-testid="alerting-rule-form"
      data-mode={rule ? "edit" : "create"}
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <div className="space-y-1">
          <label className="text-xs font-medium" htmlFor="alert-rule-name">
            Rule name
          </label>
          <Input
            id="alert-rule-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="high-cpu"
            data-testid="alert-rule-name"
          />
        </div>
        <div className="space-y-1">
          <label className="text-xs font-medium" htmlFor="alert-rule-for">
            For (seconds; 0 = fire immediately)
          </label>
          <Input
            id="alert-rule-for"
            value={forSeconds}
            onChange={(e) => setForSeconds(e.target.value)}
            inputMode="numeric"
            data-testid="alert-rule-for"
          />
        </div>
        <div className="space-y-1">
          <label className="text-xs font-medium" htmlFor="alert-rule-severity">
            Severity
          </label>
          <Input
            id="alert-rule-severity"
            value={severity}
            onChange={(e) => setSeverity(e.target.value)}
            placeholder="warning"
            data-testid="alert-rule-severity"
          />
        </div>
      </div>
      <div className="space-y-1">
        <label className="text-xs font-medium" htmlFor="alert-rule-expr">
          PromQL expression (fires when it returns any series)
        </label>
        <textarea
          id="alert-rule-expr"
          className="flex min-h-[64px] w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm"
          value={expr}
          onChange={(e) => setExpr(e.target.value)}
          placeholder='up{job="fleetly-cadvisor"} == 0'
          data-testid="alert-rule-expr"
        />
        <TestResult expr={expr} />
      </div>
      <div className="space-y-1">
        <span className="text-xs font-medium">
          Channels (unchecked = deliver to all enabled endpoints)
        </span>
        <div className="flex flex-wrap gap-3" data-testid="alert-rule-channels">
          {(endpoints.data?.endpoints ?? []).map((ep) => (
              <label key={ep.id ?? ""} className="flex items-center gap-1.5 text-xs">
              <input
                type="checkbox"
                checked={ep.id !== undefined && channelIds.includes(ep.id)}
                onChange={(e) =>
                  setChannelIds((prev) =>
                    e.target.checked
                      ? [...prev, ep.id ?? ""]
                      : prev.filter((id) => id !== ep.id),
                  )
                }
              />
              {ep.name}
            </label>
          ))}
          {(endpoints.data?.endpoints ?? []).length === 0 ? (
            <span className="text-xs text-muted-foreground">
              no notification endpoints configured yet
            </span>
          ) : null}
        </div>
        {rule ? (
          // PUT 部分更新语义的诚实披露：channels 空 = 不变——勾选全清无法
          // 表达「回落全端点」（服务端口径需 Delete+Create）。
          <span className="block text-xs text-muted-foreground">
            Editing uses partial-update semantics: clearing every channel keeps
            the current binding (delete and recreate the rule to fall back to
            all endpoints).
          </span>
        ) : null}
      </div>
      {save.isError ? (
        <EnvelopeAlertFrom envelope={errorEnvelopeFrom(save.error)} />
      ) : null}
      <div className="flex gap-2">
        <Button
          type="submit"
          size="sm"
          disabled={name.trim() === "" || expr.trim() === "" || save.isPending}
          data-testid="alert-rule-submit"
        >
          {rule ? "Save changes" : "Create rule"}
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

// 单条规则行（name/expr/for/severity/channels 摘要 + 编辑 + 删除）。
function RuleRow({ rule, canWrite }: { rule: AlertRuleView; canWrite: boolean }) {
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteAlertRule(rule.id ?? ""),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["alerting"] }),
  });
  // for_duration_seconds 线格式 = int64 的 JSON string 形态（protojson 映射）。
  const forSeconds = Number(rule.for_duration_seconds ?? 0);

  // 编辑态：行原位替换为预填表单（沿 scaling 卡「行内进编辑」形态）。
  if (editing) {
    return <RuleForm rule={rule} onClose={() => setEditing(false)} />;
  }

  return (
    <div
      className="flex flex-wrap items-center justify-between gap-2 rounded-md border p-3"
      data-testid="alerting-rule-row"
    >
      <div className="min-w-0">
        <div className="text-sm font-medium">
          {rule.name}
          {rule.labels?.severity ? (
            <span className="ml-2 text-xs text-muted-foreground">
              severity: {rule.labels.severity}
            </span>
          ) : null}
          {forSeconds > 0 ? (
            <span className="ml-2 text-xs text-muted-foreground">for {forSeconds}s</span>
          ) : null}
        </div>
        <code className="block truncate text-xs text-muted-foreground">{rule.expr}</code>
        <div className="text-xs text-muted-foreground">
          {rule.channels && rule.channels.length > 0
            ? `channels: ${rule.channels.join(", ")}`
            : "channels: all enabled endpoints"}
        </div>
      </div>
      {canWrite ? (
        <div className="flex gap-2">
          <Button
            size="sm"
            variant="outline"
            data-testid="alert-rule-edit"
            onClick={() => setEditing(true)}
          >
            Edit
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            Remove
          </Button>
        </div>
      ) : null}
      {remove.isError ? <EnvelopeAlertFrom envelope={errorEnvelopeFrom(remove.error)} /> : null}
    </div>
  );
}

export function AlertingSettingsCard() {
  const queryClient = useQueryClient();
  const [showForm, setShowForm] = useState(false);
  // 写面门（2026-09-25 走查）：模式切换/规则 CRUD/Test 都是平台管理员面
  //（requirePlatformWriteFace 硬门）——非平台管理员隐藏写钮、原位说明；
  // 状态与规则读面（read scope）全角色保留。
  const isPlatformAdmin = useIsPlatformAdmin();
  const status = useQuery({
    queryKey: ["alerting", "status"],
    queryFn: getAlertsStatus,
    refetchInterval: 10000,
  });
  const rules = useQuery({
    queryKey: ["alerting", "rules"],
    queryFn: listAlertRules,
  });
  // metrics.mode 前置门的可见面：off 时开关禁用 + 指引（设计 §2.1）。
  const metrics = useQuery({
    queryKey: ["metrics", "status"],
    queryFn: getMetricsStatus,
  });
  const metricsOn = (metrics.data?.mode ?? "") === "on";

  const setMode = useMutation({
    mutationFn: (mode: "unset" | "on") => setAlertsMode(mode),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["alerting"] }),
  });

  const mode = status.data?.mode ?? "";
  const isOn = mode === "on";
  const rulesList = rules.data?.rules ?? [];

  return (
    <Card data-testid="alerting-status-card">
      <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
        <BellRing aria-hidden className="h-4 w-4 text-muted-foreground" />
        <CardTitle className="text-sm font-semibold">Alerting</CardTitle>
        <CardDescription className="ml-auto text-xs">
          {isOn
            ? `${status.data?.rule_count ?? 0} rules · vmalert ${
                status.data?.vmalert_exists ? "deployed" : "converging"
              }`
            : "opt-in — no rule evaluator by default"}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 pt-4">
        {status.isError ? (
          <EnvelopeAlertFrom envelope={errorEnvelopeFrom(status.error)} />
        ) : (
          <>
            <div className="flex flex-wrap items-center justify-between gap-3 text-sm">
              <div>
                <div className="font-medium">alerts.mode: {mode}</div>
                <div className="text-xs text-muted-foreground">
                  {status.data?.mode_set ? `explicitly set` : `default (unset)`} · metrics.mode:{" "}
                  {status.data?.metrics_mode ?? "unset"}
                </div>
              </div>
              {isPlatformAdmin ? (
                <div className="flex items-center gap-2" data-testid="alerts-mode-toggle">
                  <Button
                    size="sm"
                    variant={isOn ? "outline" : "default"}
                    disabled={isOn || !metricsOn || setMode.isPending}
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
              ) : (
                // 写面说明（2026-09-25 走查）：非平台管理员原位只读说明。
                <p
                  className="text-xs text-muted-foreground"
                  data-testid="alerts-mode-readonly-note"
                >
                  {PLATFORM_ADMIN_NOTE}
                </p>
              )}
            </div>
            {!metricsOn ? (
              <p className="text-xs text-amber-600 dark:text-amber-400" data-testid="alerts-metrics-gate-note">
                Enable the Metrics stack first — the rule evaluator (vmalert) has no
                datasource without VictoriaMetrics.
              </p>
            ) : null}

            {setMode.isError ? (
              <EnvelopeAlertFrom envelope={errorEnvelopeFrom(setMode.error)} />
            ) : null}

            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <div className="text-sm font-medium">Alert rules</div>
                {isPlatformAdmin ? (
                  <Button size="sm" variant="outline" onClick={() => setShowForm((v) => !v)}>
                    {showForm ? "Close form" : "New rule"}
                  </Button>
                ) : null}
              </div>
              {!isPlatformAdmin ? (
                // 规则 CRUD 写面说明（2026-09-25 走查）：读面照常，写面收口。
                <p
                  className="text-xs text-muted-foreground"
                  data-testid="alerting-rules-readonly-note"
                >
                  {PLATFORM_ADMIN_NOTE}
                </p>
              ) : null}
              {showForm ? <RuleForm onClose={() => setShowForm(false)} /> : null}
              {rulesList.map((r) => (
                <RuleRow key={r.id} rule={r} canWrite={isPlatformAdmin} />
              ))}
              {rulesList.length === 0 && !showForm ? (
                <p className="text-xs text-muted-foreground">
                  {isPlatformAdmin
                    ? "No alert rules yet — create one to start evaluating expressions every 30s (firing and RESOLVED notifications go to the notification endpoints)."
                    : "No alert rules yet."}
                </p>
              ) : null}
              {rules.isError ? (
                <EnvelopeAlertFrom envelope={errorEnvelopeFrom(rules.error)} />
              ) : null}
            </div>

            <p className="text-xs text-muted-foreground">
              Rules render into the managed vmalert rule file on every change (content-addressed
              swarm config, scraped-config mechanics). Deliveries reuse the notification pipeline
              and never emit platform events; resolved alerts arrive with a RESOLVED prefix.
            </p>
          </>
        )}
      </CardContent>
    </Card>
  );
}
