// notifications 设置卡（E6 W5-S4 通知 Webhook；SystemPage Notifications
// 页签）：端点清单（名字/URL/订阅模式/开关/密钥指纹 + 启停/测试/轮换/
// 删除）+ 创建表单（secret 明文一次性弹显——复制 + 主动隐藏）+ 投递
// 台账抽屉（状态/尝试/响应码/下次重试）+ 终败红态。
//
// 诚实口径（observability §5）：secret 明文只在创建/轮换响应出现一次，
// 丢失只能 rotate-secret；投递失败零事件——可见面就是本卡的台账抽屉与
// system status 的 notifications 组件红。URL 允许 http（内网 receiver）
// ——表单常驻警示文案；平台不做 SSRF 过滤（单操作员信任模型）。
//
// 锚点（只增）：notifications-card / webhook-create-form /
// webhook-secret-once / webhook-deliveries / webhook-endpoint-failed。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, EyeOff } from "lucide-react";
import { useState } from "react";

import {
  createWebhookEndpoint,
  deleteWebhookEndpoint,
  listWebhookDeliveries,
  listWebhookEndpoints,
  rotateWebhookSecret,
  testWebhook,
  updateWebhookEndpoint,
} from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import type { WebhookDeliveryStatus, WebhookEndpointView } from "@/api/types";
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
import { formatTime } from "@/lib/utils";

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

/** 端点行动作面（启停/测试/轮换/删除/台账）。 */
function EndpointRowActions({ endpoint }: { endpoint: WebhookEndpointView }) {
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
        <Button
          size="sm"
          variant="ghost"
          onClick={() => setShowDeliveries((v) => !v)}
        >
          {showDeliveries ? "Hide deliveries" : "Deliveries"}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          className="text-red-600 dark:text-red-400"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          Delete
        </Button>
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

export function NotificationsSettingsCard() {
  const queryClient = useQueryClient();
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
  const [url, setUrl] = useState("");
  const [patterns, setPatterns] = useState("");
  const [createError, setCreateError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);
  const [onceSecret, setOnceSecret] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () =>
      createWebhookEndpoint({
        name: name.trim(),
        url: url.trim(),
        event_patterns: patterns
          .split(",")
          .map((p) => p.trim())
          .filter(Boolean),
      }),
    onSuccess: (resp) => {
      setOnceSecret(resp.secret ?? null);
      setName("");
      setUrl("");
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
        <CardTitle className="text-sm font-semibold">Notifications (webhooks)</CardTitle>
        <CardDescription className="ml-auto text-xs">
          event subscriptions delivered as signed POSTs · retries 30s/5m · 3 attempts
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4 pt-4">
        {endpoints.isError ? (
          <EnvelopeAlertFrom envelope={errorEnvelopeFrom(endpoints.error)} />
        ) : endpoints.isPending ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : (endpoints.data?.endpoints ?? []).length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No webhook endpoints. Create one below — deliveries never emit events
            (no self-trigger loops), failures surface in the ledger and the
            notifications component.
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>URL</TableHead>
                <TableHead>Patterns</TableHead>
                <TableHead>Secret</TableHead>
                <TableHead>State</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(endpoints.data?.endpoints ?? []).map((e) => (
                <TableRow key={e.id}>
                  <TableCell className="align-top font-medium">{e.name}</TableCell>
                  <TableCell
                    className="max-w-[220px] truncate align-top font-mono text-xs"
                    title={e.url ?? ""}
                  >
                    {e.url}
                  </TableCell>
                  <TableCell className="align-top font-mono text-xs">
                    {(e.event_patterns ?? []).join(", ")}
                  </TableCell>
                  <TableCell className="align-top font-mono text-xs">
                    {e.secret_fingerprint}…
                  </TableCell>
                  <TableCell className="space-y-1 align-top">
                    {failingIds.has(e.id) ? (
                      <div
                        data-testid="webhook-endpoint-failed"
                        className="text-xs font-medium text-red-600 dark:text-red-400"
                      >
                        terminally failing
                      </div>
                    ) : null}
                    <div className={e.enabled ? "text-xs" : "text-xs text-muted-foreground"}>
                      {e.enabled ? "enabled" : "disabled"}
                    </div>
                    <EndpointRowActions endpoint={e} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}

        {onceSecret ? (
          <SecretOncePanel secret={onceSecret} onDismiss={() => setOnceSecret(null)} />
        ) : null}

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
              <Label htmlFor="webhook-url">Receiver URL</Label>
              <Input
                id="webhook-url"
                placeholder="https://hooks.example.test/fleetly"
                className="font-mono text-xs"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
              />
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
          <p className="text-xs text-muted-foreground">{HTTP_NOTE}</p>
          {createError ? <EnvelopeAlertFrom envelope={createError} /> : null}
          <Button
            size="sm"
            disabled={!name.trim() || !url.trim() || !patterns.trim() || create.isPending}
            onClick={() => create.mutate()}
          >
            Create endpoint
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
