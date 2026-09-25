// 自动扩缩策略卡（W5-S1，设计 §1.1「Console：App 详情增 Scaling 卡（admin
// 可写）」+ §1.2 前置口径）：per compose service 的策略展示/编辑/删除。
//   - 前置披露：metrics.mode ≠ on → 休眠态提示（策略不动作——scaling.dormant
//     事件的服务端披露在 Console 侧的静态对应面）；
//   - 前端角色门 = admin+（设计 §1.1「admin 可写」；服务端硬门是 deploy
//     scope + 项目角色门——403 信封照实展示，前端门只是体验优化）；
//   - 服务清单来自最近 active revision 的归一化快照（AppOverviewPage 传入
//     长驻服务集——cron 服务无副本语义，不进本卡）。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Gauge, Pencil, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";

import { getMetricsStatus, getScalingPolicy, removeScalingPolicy, setScalingPolicy } from "@/api/endpoints";
import { errorEnvelopeFrom, isApiError } from "@/api/errors";
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
import { Label } from "@/components/ui/label";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useTeamCapabilities, useIsPlatformAdmin } from "@/lib/context";

interface PolicyFormValues {
  min_replicas: number;
  max_replicas: number;
  target_cpu_pct: number;
  target_mem_pct: number;
  cooldown_seconds: number;
}

const DEFAULT_FORM: PolicyFormValues = {
  min_replicas: 1,
  max_replicas: 4,
  target_cpu_pct: 60,
  target_mem_pct: 70,
  cooldown_seconds: 180,
};

// ScalingRow 是单服务的策略行（每行独立的策略查询与编辑态——服务集动态，
// hooks 按组件实例挂载）。
function ScalingRow({
  app,
  service,
  canWrite,
  metricsOn,
}: {
  app: string;
  service: string;
  canWrite: boolean;
  metricsOn: boolean;
}) {
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [form, setForm] = useState<PolicyFormValues>(DEFAULT_FORM);
  const [formError, setFormError] = useState<string>("");

  const policy = useQuery({
    queryKey: ["scaling", "policy", app, service],
    queryFn: () => getScalingPolicy(app, service),
    retry: false,
  });
  // 404 = 未配置（无策略）——不是错误态，如实呈现「no policy」。
  const notFound = isApiError(policy.error) && policy.error.status === 404;

  const save = useMutation({
    mutationFn: (values: PolicyFormValues) => setScalingPolicy(app, service, values),
    onSuccess: () => {
      setEditing(false);
      setFormError("");
      void queryClient.invalidateQueries({ queryKey: ["scaling", "policy", app, service] });
    },
    onError: (err) => setFormError(errorEnvelopeFrom(err).message ?? "failed to save policy"),
  });
  const remove = useMutation({
    mutationFn: () => removeScalingPolicy(app, service),
    onSuccess: () => {
      setFormError("");
      void queryClient.invalidateQueries({ queryKey: ["scaling", "policy", app, service] });
    },
    onError: (err) => setFormError(errorEnvelopeFrom(err).message ?? "failed to remove policy"),
  });

  const startEdit = () => {
    const p = policy.data;
    setForm(
      p
        ? {
            min_replicas: p.min_replicas ?? DEFAULT_FORM.min_replicas,
            max_replicas: p.max_replicas ?? DEFAULT_FORM.max_replicas,
            target_cpu_pct: p.target_cpu_pct ?? DEFAULT_FORM.target_cpu_pct,
            target_mem_pct: p.target_mem_pct ?? DEFAULT_FORM.target_mem_pct,
            cooldown_seconds: p.cooldown_seconds ?? DEFAULT_FORM.cooldown_seconds,
          }
        : DEFAULT_FORM,
    );
    setFormError("");
    setEditing(true);
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (form.max_replicas < form.min_replicas) {
      setFormError("max replicas must be >= min replicas");
      return;
    }
    if (form.target_cpu_pct === 0 && form.target_mem_pct === 0) {
      setFormError("at least one target is required (cpu or mem)");
      return;
    }
    save.mutate(form);
  };

  if (policy.isLoading) {
    return (
      <TableRow data-testid="scaling-row" data-service={service}>
        <TableCell className="font-mono text-xs">{service}</TableCell>
        <TableCell className="text-xs text-muted-foreground">Loading…</TableCell>
        <TableCell />
      </TableRow>
    );
  }

  if (editing) {
    const num = (label: string, key: keyof PolicyFormValues, hint: string) => (
      <div className="space-y-1">
        <Label className="text-xs">{label}</Label>
        <Input
          type="number"
          className="h-8 w-20 text-xs"
          data-testid={`scaling-input-${key}`}
          value={form[key]}
          onChange={(e) => setForm({ ...form, [key]: Number(e.target.value) })}
        />
        <span className="block text-[10px] text-muted-foreground">{hint}</span>
      </div>
    );
    return (
      <TableRow data-testid="scaling-row" data-service={service} data-editing="true">
        <TableCell className="font-mono text-xs">{service}</TableCell>
        <TableCell colSpan={2}>
          <form className="flex flex-wrap items-end gap-3" onSubmit={onSubmit}>
            {num("Min", "min_replicas", "≥1")}
            {num("Max", "max_replicas", "≤16")}
            {num("CPU %", "target_cpu_pct", "20-90, 0=off")}
            {num("Mem %", "target_mem_pct", "20-90, 0=off")}
            {num("Cooldown s", "cooldown_seconds", "60-3600")}
            <div className="flex items-center gap-2 pb-1">
              <Button type="submit" size="sm" data-testid="scaling-save" disabled={save.isPending}>
                {save.isPending ? "Saving…" : "Save"}
              </Button>
              <Button type="button" size="sm" variant="ghost" onClick={() => setEditing(false)}>
                Cancel
              </Button>
            </div>
            {formError ? (
              <span className="w-full text-xs text-red-600 dark:text-red-400" data-testid="scaling-form-error">
                {formError}
              </span>
            ) : null}
          </form>
        </TableCell>
      </TableRow>
    );
  }

  const p = notFound ? undefined : policy.data;
  return (
    <TableRow data-testid="scaling-row" data-service={service} data-policy={p ? "set" : "none"}>
      <TableCell className="font-mono text-xs">{service}</TableCell>
      {p ? (
        <>
          <TableCell className="text-xs">
            <span data-testid="scaling-policy-summary">
              {p.min_replicas}–{p.max_replicas} replicas · cpu {p.target_cpu_pct === 0 ? "off" : `${p.target_cpu_pct}%`}
              {" · mem "}
              {p.target_mem_pct === 0 ? "off" : `${p.target_mem_pct}%`} · cooldown {p.cooldown_seconds}s
            </span>
            {!metricsOn ? (
              <span
                className="ml-2 inline-flex items-center rounded-md border px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground"
                data-testid="scaling-row-dormant"
              >
                dormant (metrics off)
              </span>
            ) : null}
          </TableCell>
          <TableCell className="whitespace-nowrap text-right">
            {canWrite ? (
              <>
                <Button variant="ghost" size="icon" className="h-7 w-7" aria-label={`Edit policy of ${service}`} onClick={startEdit}>
                  <Pencil aria-hidden className="h-3.5 w-3.5" />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 text-red-600 dark:text-red-400"
                  aria-label={`Remove policy of ${service}`}
                  disabled={remove.isPending}
                  onClick={() => remove.mutate()}
                >
                  <Trash2 aria-hidden className="h-3.5 w-3.5" />
                </Button>
              </>
            ) : null}
          </TableCell>
        </>
      ) : (
        <>
          <TableCell className="text-xs text-muted-foreground">
            No policy (compose replica count is kept).
            {!metricsOn ? (
              <span
                className="ml-2 inline-flex items-center rounded-md border px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground"
                data-testid="scaling-row-dormant"
              >
                dormant (metrics off)
              </span>
            ) : null}
          </TableCell>
          <TableCell className="text-right">
            {canWrite ? (
              <Button variant="ghost" size="sm" onClick={startEdit}>
                Add policy
              </Button>
            ) : null}
          </TableCell>
        </>
      )}
    </TableRow>
  );
}

interface AppScalingCardProps {
  app: string;
  /** services 是长驻服务名集（cron 服务无副本语义，由调用方排除）。 */
  services: string[];
}

export function AppScalingCard({ app, services }: AppScalingCardProps) {
  // 前端角色门（admin+ 可写——设计 §1.1；服务端硬门不变，403 信封照实展
  // 示）。平台管理员资源面恒只读（P0-3 双门）——编辑钮消失时以说明行明示
  // 原因，不做静默消失。
  const { canAdminResources } = useTeamCapabilities();
  const isPlatformAdmin = useIsPlatformAdmin();
  const status = useQuery({
    queryKey: ["metrics", "status"],
    queryFn: getMetricsStatus,
    staleTime: 10_000,
  });
  const metricsOn = status.data?.mode === "on";

  return (
    <Card data-testid="app-scaling-card">
      <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
        <Gauge aria-hidden className="h-4 w-4 text-muted-foreground" />
        <CardTitle className="text-sm font-semibold">Autoscaling</CardTitle>
        <CardDescription className="ml-auto text-xs">per-service CPU/memory policies</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 pt-4">
        {status.isError ? <EnvelopeAlertFrom envelope={errorEnvelopeFrom(status.error)} /> : null}
        {!metricsOn ? (
          <p className="text-xs text-muted-foreground" data-testid="scaling-dormant-hint">
            metrics.mode is not on — policies are dormant and never act. Enable the metrics
            stack (Resources card) for evaluation to resume; a one-time dormant disclosure is
            emitted per policy.
          </p>
        ) : null}
        {!canAdminResources && isPlatformAdmin ? (
          // P0-3：平台管理员资源面只读——说明行（CLI 有 scaling 策略命令，
          // 文案如实指路）。
          <p className="text-xs text-muted-foreground" data-testid="platform-readonly-note">
            Platform administrators have read-only access to resources
            (separation of duties). Manage autoscaling policies from the CLI
            with a machine token, or ask a team owner for a member role.
          </p>
        ) : null}
        {services.length === 0 ? (
          <p className="text-xs text-muted-foreground">No long-running services deployed yet.</p>
        ) : (
          <div className="rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-1/4">Service</TableHead>
                  <TableHead>Policy</TableHead>
                  <TableHead className="w-24 text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {services.map((s) => (
                  <ScalingRow key={s} app={app} service={s} canWrite={canAdminResources} metricsOn={metricsOn} />
                ))}
              </TableBody>
            </Table>
          </div>
        )}
        <p className="text-xs text-muted-foreground">
          The autoscaler adjusts replicas toward the CPU/memory targets (clamped to min/max,
          cooldown-aware). Services with volumes scale up only — never shrink. Dimensions
          without a resource limit in compose are not evaluated (no denominator is guessed).
        </p>
      </CardContent>
    </Card>
  );
}
