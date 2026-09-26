// notifications 设置卡（E6 W5-S4 通知 Webhook；W4-S3 通道扩展；SystemPage
// Notifications 页签）：端点清单（名字/类型/URL/订阅模式/开关/密钥指纹 +
// 启停/测试/轮换/删除）+ 创建表单（通道类型选择——webhook/slack 走 URL、
// email 走收件地址 + 平台 SMTP 设置；secret 明文一次性弹显——复制 + 主动
// 隐藏）+ 投递台账抽屉（状态/尝试/响应码/下次重试）+ 终败红态 + 平台级
// SMTP 设置卡（email 端点共用一份；密码只写不读——读面只出指纹）。
//
// 诚实口径（observability §5/§8）：secret 明文只在创建/轮换响应出现一次，
// 丢失只能 rotate-secret；投递失败零事件——可见面就是本卡的台账抽屉与
// system status 的 notifications 组件红。URL 允许 http（内网 receiver）
// ——表单常驻警示文案；平台不做 SSRF 过滤（单操作员信任模型）。SMTP
// 密码明文只写不读，保存响应只回指纹。
//
// 锚点（只增）：notifications-card / webhook-create-form /
// webhook-secret-once / webhook-deliveries / webhook-endpoint-failed /
// notifications-smtp-card / smtp-test-result。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, EyeOff, Mail } from "lucide-react";
import { useState } from "react";

import {
  createWebhookEndpoint,
  deleteWebhookEndpoint,
  getSmtpSettings,
  listWebhookDeliveries,
  listWebhookEndpoints,
  rotateWebhookSecret,
  testSmtp,
  testWebhook,
  updateSmtpSettings,
  updateWebhookEndpoint,
} from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import type {
  SmtpSettingsView,
  WebhookChannelType,
  WebhookDeliveryStatus,
  WebhookEndpointView,
} from "@/api/types";
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatTime } from "@/lib/utils";
import { useIsPlatformAdmin } from "@/lib/context";

/** 非平台管理员的写面说明（2026-09-25 走查——此前 viewer/developer 见到
 * enabled 的端点 CRUD/Test/SMTP 保存假按钮）：台账读面保留，写面原位说明。 */
const PLATFORM_ADMIN_NOTE = "Platform administrator required.";

/** http 明文警示（内网 receiver 允许 http——常驻文案，设计 §5.1）。 */
const HTTP_NOTE =
  "https is strongly recommended; plain http is allowed for intranet receivers. The platform does not filter destinations (single-operator trust model) — the receiver URL is your own choice.";

/** 一次性 secret 警示（创建/轮换响应的一次性语义）。 */
const SECRET_ONCE_NOTE =
  "Store it now — the platform keeps only a fingerprint, and this value is never shown again. Rotate to replace it.";

function CopySecret({ secret }: { secret: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      size="sm"
      variant="outline"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(secret);
          setCopied(true);
        } catch {
          setCopied(false);
        }
      }}
    >
      {copied ? "Copied" : "Copy secret"}
    </Button>
  );
}

/** 一次性 secret 弹显面板（复制 + 「不再显示」）。 */
function SecretOncePanel({ secret, onDismiss }: { secret: string; onDismiss: () => void }) {
  return (
    <div
      data-testid="webhook-secret-once"
      className="space-y-2 rounded-md border border-amber-500/40 bg-amber-500/5 p-3"
    >
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-semibold text-amber-800 dark:text-amber-300">
          Signing secret (shown only once)
        </span>
        <div className="flex items-center gap-2">
          <CopySecret secret={secret} />
          <Button size="sm" variant="ghost" onClick={onDismiss}>
            <EyeOff aria-hidden className="h-3.5 w-3.5" />
            Hide
          </Button>
        </div>
      </div>
      <code className="block break-all font-mono text-xs">{secret}</code>
      <p className="text-xs text-muted-foreground">{SECRET_ONCE_NOTE}</p>
    </div>
  );
}

/** 投递台账抽屉（单端点）——重试路径与终败的诚实可见面。 */
function DeliveriesPanel({ endpointId }: { endpointId: string }) {
  const deliveries = useQuery({
    queryKey: ["notifications", "deliveries", endpointId],
    queryFn: () => listWebhookDeliveries({ endpoint_id: endpointId, limit: 20 }),
    refetchInterval: 10000,
  });
  return (
    <div data-testid="webhook-deliveries" className="space-y-2 rounded-md border p-3">
      <div className="text-xs font-semibold">Delivery ledger (latest 20)</div>
      {deliveries.isError ? (
        <EnvelopeAlertFrom envelope={errorEnvelopeFrom(deliveries.error)} />
      ) : (deliveries.data?.deliveries ?? []).length === 0 ? (
        <p className="text-xs text-muted-foreground">No deliveries yet.</p>
      ) : (
        <div className="space-y-1">
          {(deliveries.data?.deliveries ?? []).map((d) => (
            <div
              key={d.id}
              className="flex flex-wrap items-center gap-2 rounded border px-2 py-1 text-xs"
              data-testid="webhook-delivery-row"
            >
              <span
                className={
                  d.status === "ok"
                    ? "font-medium text-emerald-600 dark:text-emerald-400"
                    : d.status === "failed"
                      ? "font-medium text-red-600 dark:text-red-400"
                      : "font-medium text-amber-600 dark:text-amber-400"
                }
              >
                {d.status}
              </span>
              <span>event_seq {d.event_seq}</span>
              <span>attempts {d.attempts}</span>
              {d.response_code != null ? <span>response {d.response_code}</span> : null}
              {d.next_retry_at ? <span>retry {formatTime(d.next_retry_at)}</span> : null}
              {d.last_error ? (
                <span className="truncate text-muted-foreground" title={d.last_error}>
                  {d.last_error}
                </span>
              ) : null}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

/** 端点行动作面（启停/测试/轮换/删除/台账）。写面（Enable/Test/Rotate/
 * Delete = admin scope + 平台写面）按 canWrite 渲染；Deliveries 抽屉是
 * read scope 读面——全角色保留（2026-09-25 走查角色门拆分）。 */
function EndpointRowActions({ endpoint, canWrite }: { endpoint: WebhookEndpointView; canWrite: boolean }) {
  const queryClient = useQueryClient();
  const [showDeliveries, setShowDeliveries] = useState(false);
  const [testError, setTestError] = useState<string | null>(null);
  const [testOk, setTestOk] = useState<boolean | null>(null);
  // 生成类型的字段全部可选（proto3 零值不输出）——行内收敛非空局部，
  // 缺失 id 的视图理论不可达（行来自端点清单投影）。
  const id = endpoint.id ?? "";
  const enabled = endpoint.enabled ?? false;

  const invalidate = () =>
    void queryClient.invalidateQueries({ queryKey: ["notifications"] });

  const toggle = useMutation({
    mutationFn: (next: boolean) => updateWebhookEndpoint(id, { enabled: next }),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: () => deleteWebhookEndpoint(id),
    onSuccess: invalidate,
  });
  const rotate = useMutation({
    mutationFn: () => rotateWebhookSecret(id),
    onSuccess: invalidate,
  });
  const test = useMutation({
    mutationFn: () => testWebhook(id),
    onSuccess: (resp) => {
      setTestError(
        resp.ok ? null : resp.error || `receiver answered ${resp.status_code ?? 0}`,
      );
      setTestOk(resp.ok ?? null);
    },
    onError: (err) => {
      setTestOk(false);
      setTestError(errorEnvelopeFrom(err).message ?? String(err));
    },
  });
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-1.5">
        {canWrite ? (
          <>
            <Button
              size="sm"
              variant="outline"
              disabled={toggle.isPending}
              onClick={() => toggle.mutate(!enabled)}
            >
              {enabled ? "Disable" : "Enable"}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={test.isPending}
              onClick={() => test.mutate()}
            >
              Test
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={rotate.isPending}
              onClick={() => rotate.mutate()}
            >
              Rotate secret
            </Button>
          </>
        ) : null}
        <Button
          size="sm"
          variant="ghost"
          onClick={() => setShowDeliveries((v) => !v)}
        >
          {showDeliveries ? "Hide deliveries" : "Deliveries"}
        </Button>
        {canWrite ? (
          <Button
            size="sm"
            variant="ghost"
            className="text-red-600 dark:text-red-400"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            Delete
          </Button>
        ) : null}
      </div>
      {testOk === false && testError ? (
        <p className="text-xs text-red-600 dark:text-red-400" data-testid="webhook-test-error">
          Test failed: {testError}
        </p>
      ) : null}
      {testOk === true ? (
        <p className="text-xs text-emerald-600 dark:text-emerald-400" data-testid="webhook-test-ok">
          Test payload delivered (2xx).
        </p>
      ) : null}
      {rotate.data?.secret ? (
        <SecretOncePanel
          secret={rotate.data.secret}
          onDismiss={() => rotate.reset()}
        />
      ) : null}
      {showDeliveries ? <DeliveriesPanel endpointId={id} /> : null}
    </div>
  );
}

/** 通道类型选项（W4-S3 词表三值；webhook 缺省——存量端点升级即 webhook）。 */
const CHANNEL_OPTIONS: { value: WebhookChannelType; label: string }[] = [
  { value: "webhook", label: "webhook (signed JSON POST)" },
  { value: "slack", label: "slack (Incoming Webhook)" },
  { value: "email", label: "email (SMTP via platform settings)" },
];

export function NotificationsSettingsCard() {
  const queryClient = useQueryClient();
  // 写面门（2026-09-25 走查）：端点 CRUD/Test = admin scope + 平台写面
  // ——非平台管理员隐藏创建表单与行写钮、原位说明；端点清单与投递台账
  //（read scope）全角色保留。
  const isPlatformAdmin = useIsPlatformAdmin();
  const endpoints = useQuery({
    queryKey: ["notifications", "endpoints"],
    queryFn: listWebhookEndpoints,
    refetchInterval: 15000,
  });
  // 终败红面：最近终态 = failed 的端点集（台账是事实源——组件红面的
  // Console 投影；投递失败零事件，防自激励环）。
  const failed = useQuery({
    queryKey: ["notifications", "failed"],
    queryFn: () => listWebhookDeliveries({ status: "failed" as WebhookDeliveryStatus, limit: 100 }),
    refetchInterval: 15000,
  });
  const failingIds = new Set((failed.data?.deliveries ?? []).map((d) => d.endpoint_id));

  const [name, setName] = useState("");
  const [channel, setChannel] = useState<WebhookChannelType>("webhook");
  const [url, setUrl] = useState("");
  const [target, setTarget] = useState("");
  const [patterns, setPatterns] = useState("");
  const [createError, setCreateError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);
  const [onceSecret, setOnceSecret] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () =>
      createWebhookEndpoint({
        name: name.trim(),
        url: channel === "email" ? "" : url.trim(),
        event_patterns: patterns
          .split(",")
          .map((p) => p.trim())
          .filter(Boolean),
        type: channel,
        target: channel === "email" ? target.trim() : "",
      }),
    onSuccess: (resp) => {
      setOnceSecret(resp.secret ?? null);
      setName("");
      setUrl("");
      setTarget("");
      setPatterns("");
      setCreateError(null);
      void queryClient.invalidateQueries({ queryKey: ["notifications"] });
    },
    onError: (err) => {
      setOnceSecret(null);
      setCreateError(errorEnvelopeFrom(err));
    },
  });

  return (
    <Card data-testid="notifications-card">
      <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
        <Bell aria-hidden className="h-4 w-4 text-muted-foreground" />
        <CardTitle className="text-sm font-semibold">Notifications</CardTitle>
        <CardDescription className="ml-auto text-xs">
          event subscriptions delivered over webhook / slack / email · retries 30s/5m · 3 attempts
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4 pt-4">
        {endpoints.isError ? (
          <EnvelopeAlertFrom envelope={errorEnvelopeFrom(endpoints.error)} />
        ) : endpoints.isPending ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : (endpoints.data?.endpoints ?? []).length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No notification endpoints. Create one below — deliveries never emit events
            (no self-trigger loops), failures surface in the ledger and the
            notifications component.
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Channel</TableHead>
                <TableHead>Destination</TableHead>
                <TableHead>Patterns</TableHead>
                <TableHead>Secret</TableHead>
                <TableHead>State</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(endpoints.data?.endpoints ?? []).map((e) => (
                <EndpointRow key={e.id} endpoint={e} failing={failingIds.has(e.id)} canWrite={isPlatformAdmin} />
              ))}
            </TableBody>
          </Table>
        )}

        {onceSecret ? (
          <SecretOncePanel secret={onceSecret} onDismiss={() => setOnceSecret(null)} />
        ) : null}

        {isPlatformAdmin ? (
          <div className="space-y-3 rounded-md border p-3" data-testid="webhook-create-form">
          <div className="text-sm font-medium">Create endpoint</div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div className="space-y-1.5">
              <Label htmlFor="webhook-name">Name</Label>
              <Input
                id="webhook-name"
                placeholder="ops-slack"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="webhook-channel">Channel</Label>
              <Select value={channel} onValueChange={(v) => setChannel(v as WebhookChannelType)}>
                <SelectTrigger id="webhook-channel" data-testid="webhook-channel-select">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {CHANNEL_OPTIONS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="webhook-patterns">Event patterns</Label>
              <Input
                id="webhook-patterns"
                placeholder="deployment.*, cron.failed or * for everything"
                className="font-mono text-xs"
                value={patterns}
                onChange={(e) => setPatterns(e.target.value)}
              />
            </div>
          </div>
          {channel === "email" ? (
            <div className="space-y-1.5">
              <Label htmlFor="webhook-target">Recipient mailbox</Label>
              <Input
                id="webhook-target"
                placeholder="ops@example.test"
                className="font-mono text-xs"
                value={target}
                onChange={(e) => setTarget(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Delivery goes through the platform SMTP settings below — save them once
                and every email endpoint reuses them.
              </p>
            </div>
          ) : (
            <div className="space-y-1.5">
              <Label htmlFor="webhook-url">Receiver URL</Label>
              <Input
                id="webhook-url"
                placeholder={channel === "slack" ? "https://hooks.slack.com/services/…" : "https://hooks.example.test/fleetly"}
                className="font-mono text-xs"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
              />
            </div>
          )}
          <p className="text-xs text-muted-foreground">{HTTP_NOTE}</p>
          {createError ? <EnvelopeAlertFrom envelope={createError} /> : null}
          <Button
            size="sm"
            disabled={
              !name.trim() ||
              !patterns.trim() ||
              (channel === "email" ? !target.trim() : !url.trim()) ||
              create.isPending
            }
            onClick={() => create.mutate()}
          >
            Create endpoint
          </Button>
        </div>
        ) : (
          // 写面说明（2026-09-25 走查）：非平台管理员原位只读说明。
          <p className="text-xs text-muted-foreground" data-testid="webhook-create-readonly-note">
            {PLATFORM_ADMIN_NOTE}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

/** 端点行（类型/目的地列 + 行动作面）——通道语义投影（W4-S3）。 */
function EndpointRow({ endpoint, failing, canWrite }: { endpoint: WebhookEndpointView; failing: boolean; canWrite: boolean }) {
  const type = endpoint.type ?? "webhook";
  return (
    <TableRow>
      <TableCell className="align-top font-medium">{endpoint.name}</TableCell>
      <TableCell className="align-top">
        <span
          className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs"
          data-testid={`endpoint-type-${type}`}
        >
          {type}
        </span>
      </TableCell>
      <TableCell
        className="max-w-[220px] truncate align-top font-mono text-xs"
        title={type === "email" ? `to: ${endpoint.target ?? ""}` : endpoint.url ?? ""}
      >
        {type === "email" ? `to: ${endpoint.target ?? ""}` : endpoint.url}
      </TableCell>
      <TableCell className="align-top font-mono text-xs">
        {(endpoint.event_patterns ?? []).join(", ")}
      </TableCell>
      <TableCell className="align-top font-mono text-xs">
        {endpoint.secret_fingerprint}…
      </TableCell>
      <TableCell className="space-y-1 align-top">
        {failing ? (
          <div
            data-testid="webhook-endpoint-failed"
            className="text-xs font-medium text-red-600 dark:text-red-400"
          >
            terminally failing
          </div>
        ) : null}
        <div className={endpoint.enabled ? "text-xs" : "text-xs text-muted-foreground"}>
          {endpoint.enabled ? "enabled" : "disabled"}
        </div>
        <EndpointRowActions endpoint={endpoint} canWrite={canWrite} />
      </TableCell>
    </TableRow>
  );
}

/** SMTP 设置密码占位（PUT 语义：空 = 清除——诚实文案，不谎称「保持不变」）。 */
const SMTP_PASSWORD_NOTE =
  "Write-only: the password is stored encrypted and never read back (only a fingerprint is shown). Saving with an empty password clears the stored one.";

/** SmtpSettingsCard 是平台级 SMTP 设置卡（W4-S3，observability §8.3）：
 * email 端点共用一份投递配置；密码只写不读；Test 发真实测试邮件。
 * 读写面整体 admin scope（scope.go NotificationsService/GetSmtpSettings
 * 等三 RPC 登记）：非平台管理员整卡替换为只读说明（2026-09-25 走查）；
 * saved 时间戳仅在非零值时渲染（epoch 零值守卫——ACME 卡同类修复的
 * 漏网点，2026-09-25 走查）。 */
export function SmtpSettingsCard() {
  const queryClient = useQueryClient();
  const isPlatformAdmin = useIsPlatformAdmin();
  const settings = useQuery({
    queryKey: ["notifications", "smtp"],
    queryFn: getSmtpSettings,
    enabled: isPlatformAdmin,
    retry: false,
  });
  const stored: SmtpSettingsView | null = settings.data?.settings ?? null;

  const [host, setHost] = useState("");
  const [port, setPort] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [from, setFrom] = useState("");
  const [testTo, setTestTo] = useState("");
  const [saveError, setSaveError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);
  const [testOk, setTestOk] = useState<boolean | null>(null);
  const [testError, setTestError] = useState<string | null>(null);
  const [probeError, setProbeError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);

  // 表单以已存设置预填（密码除外——只写不读）；加载完成后一次。
  const [seeded, setSeeded] = useState(false);
  if (stored && !seeded) {
    setHost(stored.host ?? "");
    setPort(stored.port ? String(stored.port) : "");
    setUsername(stored.username ?? "");
    setFrom(stored.from ?? "");
    setSeeded(true);
  }

  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["notifications", "smtp"] });

  const save = useMutation({
    mutationFn: () =>
      updateSmtpSettings({
        host: host.trim(),
        port: Number(port) || 0,
        username: username.trim(),
        password,
        from: from.trim(),
      }),
    onSuccess: () => {
      setPassword("");
      setSaveError(null);
      invalidate();
    },
    onError: (err) => setSaveError(errorEnvelopeFrom(err)),
  });

  const probe = useMutation({
    mutationFn: () => testSmtp({ to: testTo.trim() }),
    onSuccess: (resp) => {
      setTestOk(resp.ok ?? false);
      setTestError(resp.ok ? null : resp.error || `relay answered ${resp.status_code ?? 0}`);
      setProbeError(null);
    },
    onError: (err) => {
      setTestOk(false);
      setTestError(null);
      setProbeError(errorEnvelopeFrom(err));
    },
  });

  // saved 时间戳零值守卫（对齐 acme-settings-card 同类修复）：epoch 0 或
  // 不可解析一律视作未保存——proto 零值 Timestamp 序列化为 epoch 字符串
  // 仍为真值，未保存过设置时会渲染 "saved 1970/…"。
  const savedAtIso = stored?.updated_at ?? "";
  const savedAt = savedAtIso && new Date(savedAtIso).getTime() > 0 ? savedAtIso : "";

  // 非平台管理员：整卡替换为只读说明（全部 hooks 之后条件返回）。
  if (!isPlatformAdmin) {
    return (
      <Card data-testid="notifications-smtp-card">
        <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
          <Mail aria-hidden className="h-4 w-4 text-muted-foreground" />
          <CardTitle className="text-sm font-semibold">SMTP settings (email channel)</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4 pt-4">
          <p
            className="text-sm text-muted-foreground"
            data-testid="smtp-settings-readonly-note"
          >
            {PLATFORM_ADMIN_NOTE}
          </p>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card data-testid="notifications-smtp-card">
      <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
        <Mail aria-hidden className="h-4 w-4 text-muted-foreground" />
        <CardTitle className="text-sm font-semibold">SMTP settings (email channel)</CardTitle>
        <CardDescription className="ml-auto text-xs">
          one platform-wide configuration shared by every email endpoint
          {savedAt ? ` · saved ${formatTime(savedAt)}` : ""}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4 pt-4">
        {settings.isError ? (
          <EnvelopeAlertFrom envelope={errorEnvelopeFrom(settings.error)} />
        ) : null}
        {stored && !stored.host ? (
          <p className="text-xs text-muted-foreground">
            No SMTP settings saved yet — email endpoints cannot deliver until they are configured.
          </p>
        ) : null}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <div className="space-y-1.5">
            <Label htmlFor="smtp-host">Relay host</Label>
            <Input
              id="smtp-host"
              placeholder="smtp.example.test"
              className="font-mono text-xs"
              value={host}
              onChange={(e) => setHost(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="smtp-port">Port</Label>
            <Input
              id="smtp-port"
              placeholder="587"
              inputMode="numeric"
              className="font-mono text-xs"
              value={port}
              onChange={(e) => setPort(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="smtp-from">From address</Label>
            <Input
              id="smtp-from"
              placeholder="fleetly@example.test"
              className="font-mono text-xs"
              value={from}
              onChange={(e) => setFrom(e.target.value)}
            />
          </div>
        </div>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="smtp-username">Username (optional)</Label>
            <Input
              id="smtp-username"
              className="font-mono text-xs"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="smtp-password">Password (write-only)</Label>
            <Input
              id="smtp-password"
              type="password"
              autoComplete="new-password"
              className="font-mono text-xs"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">{SMTP_PASSWORD_NOTE}</p>
          </div>
        </div>
        {stored?.password_fingerprint ? (
          <p className="font-mono text-xs text-muted-foreground" data-testid="smtp-password-fingerprint">
            stored password fingerprint: {stored.password_fingerprint}…
          </p>
        ) : null}
        {saveError ? <EnvelopeAlertFrom envelope={saveError} /> : null}
        <Button
          size="sm"
          disabled={!host.trim() || !port || !from.trim() || save.isPending}
          onClick={() => save.mutate()}
        >
          Save SMTP settings
        </Button>

        <div className="space-y-2 rounded-md border p-3">
          <div className="text-sm font-medium">Send a test email</div>
          <div className="flex flex-wrap items-center gap-2">
            <Input
              id="smtp-test-to"
              placeholder="you@example.test"
              className="max-w-xs font-mono text-xs"
              value={testTo}
              onChange={(e) => setTestTo(e.target.value)}
            />
            <Button
              size="sm"
              variant="outline"
              disabled={!testTo.trim() || probe.isPending}
              onClick={() => probe.mutate()}
            >
              Test
            </Button>
          </div>
          {testOk === true ? (
            <p className="text-xs text-emerald-600 dark:text-emerald-400" data-testid="smtp-test-result">
              Test mail accepted by the relay (check the mailbox).
            </p>
          ) : null}
          {testOk === false && testError ? (
            <p className="text-xs text-red-600 dark:text-red-400" data-testid="smtp-test-result">
              Test failed: {testError}
            </p>
          ) : null}
          {probeError ? <EnvelopeAlertFrom envelope={probeError} /> : null}
        </div>
      </CardContent>
    </Card>
  );
}
