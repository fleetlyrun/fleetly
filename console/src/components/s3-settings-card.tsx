// S3 设置卡（E3 对象存储 §5.1/E3-2/E3-8，设计 §5.5 锚点契约）：对象存储
// 三模式（unset/external/rustfs）的读写面 + 候选配置先测后存 + 公网子域
// 开关门禁 + RustFS「便捷层非灾备」常驻诚实标注。
//
// 服务端契约（system.proto）：
//   - 读面脱敏：secret 只回 fingerprint（sha256 前 8 hex），永无明文回填；
//   - 写面 PUT 全量语义：请求即新状态，secret 留空 = 清除；
//   - 探针：put→get→delete 单轮真实读写，候选配置（任一字段非零）可先测
//     后存；失败以 E_S3_TEST_FAILED 信封报错（失败步进 context）；
//   - public_exposed 仅 rustfs 且需 base_domain（E_S3_PUBLIC_REQUIRES_BASE_DOMAIN）。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Cloud } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import {
  getS3Settings,
  testS3Connection,
  updateS3Settings,
} from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import type { ErrorEnvelope } from "@/api/errors";
import type { S3ConnectionTestResult } from "@/api/types";
import {
  EnvelopeAlert,
  EnvelopeAlertFrom,
} from "@/components/envelope-alert";
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
import { formatTime } from "@/lib/utils";

// RustFS 诚实口径（D-S3-8，V2-2 裁决）：UI 文案语言跟随 console 现状
//（英文为工作语言）。常驻于 rustfs 模式——不可关闭、不随保存消失。
const RUSTFS_HONESTY_NOTE =
  "The on-host RustFS is a convenience layer (protection against accidental deletion and single-file corruption), not disaster recovery: if the host is lost, these backups are lost with it. For disaster recovery, configure an external S3 endpoint.";

const MODE_OPTIONS = [
  { value: "unset", label: "Not configured" },
  { value: "external", label: "External S3 endpoint" },
  { value: "rustfs", label: "Managed RustFS" },
];

/** 表单局部状态：secret 永不回填明文（输入框恒从空起）。 */
interface S3FormState {
  mode: string;
  endpointUrl: string;
  region: string;
  bucket: string;
  accessKeyId: string;
  secret: string;
  pathStyle: boolean;
  publicExposed: boolean;
}

function formFromSettings(mode: string, s?: {
  endpoint_url?: string;
  region?: string;
  bucket?: string;
  access_key_id?: string;
  path_style?: boolean;
  public_exposed?: boolean;
}): S3FormState {
  return {
    mode,
    endpointUrl: s?.endpoint_url ?? "",
    region: s?.region ?? "",
    bucket: s?.bucket ?? "",
    accessKeyId: s?.access_key_id ?? "",
    secret: "",
    pathStyle: s?.path_style ?? false,
    publicExposed: s?.public_exposed ?? false,
  };
}

/** 探针单步行（put/get/delete：ok + 耗时 + 失败原文——诚实契约可定位）。 */
function ProbeStepRow({
  step,
}: {
  step: { step?: string; ok?: boolean; duration_ms?: string; error?: string };
}) {
  return (
    <div className="flex items-center justify-between gap-3 text-xs">
      <span className="flex items-center gap-1.5">
        <span
          aria-hidden
          className={`h-2 w-2 rounded-full ${
            step.ok ? "bg-emerald-500" : "bg-red-500"
          }`}
        />
        <code>{step.step}</code>
      </span>
      <span className="flex items-center gap-2 text-muted-foreground">
        {step.error ? (
          <span className="max-w-[220px] truncate text-red-600 dark:text-red-400" title={step.error}>
            {step.error}
          </span>
        ) : null}
        {step.duration_ms !== undefined ? `${step.duration_ms} ms` : null}
      </span>
    </div>
  );
}

export function S3SettingsCard() {
  const queryClient = useQueryClient();
  const settingsQuery = useQuery({
    queryKey: ["system", "s3-settings"],
    queryFn: getS3Settings,
  });

  const stored = settingsQuery.data?.settings;
  const [form, setForm] = useState<S3FormState>(formFromSettings("unset"));
  // 服务端设置到达后同步一次表单（secret 除外——永不回填）；用户已动手
  //（dirty）则不再回写——迟到的/轮询的重读不得覆盖编辑中的表单。
  const dirtyRef = useRef(false);
  useEffect(() => {
    if (stored && !dirtyRef.current) {
      setForm(formFromSettings(stored.mode ?? "unset", stored));
    }
  }, [stored]);

  const [saveResult, setSaveResult] = useState<"saved" | null>(null);
  const [saveError, setSaveError] = useState<ErrorEnvelope | null>(null);
  const [testResult, setTestResult] = useState<S3ConnectionTestResult | null>(null);
  const [testError, setTestError] = useState<ErrorEnvelope | null>(null);

  const set = (patch: Partial<S3FormState>) => {
    dirtyRef.current = true;
    setSaveResult(null);
    setForm((prev) => ({ ...prev, ...patch }));
  };

  const saveMutation = useMutation({
    mutationFn: () =>
      updateS3Settings({
        mode: form.mode,
        endpoint_url: form.endpointUrl,
        region: form.region,
        bucket: form.bucket,
        access_key_id: form.accessKeyId,
        secret_access_key: form.secret,
        path_style: form.pathStyle,
        public_exposed: form.publicExposed,
      }),
    onSuccess: (resp) => {
      setSaveError(null);
      setSaveResult("saved");
      dirtyRef.current = false; // 保存即新基线：表单随服务端响应重置。
      if (resp.settings) {
        setForm(formFromSettings(resp.settings.mode ?? "unset", resp.settings));
      }
      void queryClient.invalidateQueries({ queryKey: ["system", "s3-settings"] });
    },
    onError: (err) => {
      setSaveResult(null);
      setSaveError(errorEnvelopeFrom(err));
    },
  });

  const testMutation = useMutation({
    mutationFn: () =>
      // 候选配置先测后存：表单任一字段非零即按候选测；全空 = 测已存配置。
      testS3Connection({
        endpoint_url: form.endpointUrl,
        region: form.region,
        bucket: form.bucket,
        access_key_id: form.accessKeyId,
        secret_access_key: form.secret,
        path_style: form.pathStyle,
      }),
    onSuccess: (resp) => {
      setTestError(null);
      setTestResult(resp.result ?? null);
    },
    onError: (err) => {
      setTestResult(null);
      setTestError(errorEnvelopeFrom(err));
    },
  });

  const isRustfs = form.mode === "rustfs";
  const isExternal = form.mode === "external";

  return (
    <Card data-testid="s3-settings-card">
      <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
        <Cloud aria-hidden className="h-4 w-4 text-muted-foreground" />
        <CardTitle className="text-sm font-semibold">Object storage (S3)</CardTitle>
        <CardDescription className="ml-auto text-xs">
          Remote backup uploads{stored?.updated_at ? ` · saved ${formatTime(stored.updated_at)}` : ""}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4 pt-4">
        {settingsQuery.isError ? (
          <EnvelopeAlertFrom envelope={errorEnvelopeFrom(settingsQuery.error)} />
        ) : null}

        <div className="space-y-1.5">
          <Label htmlFor="s3-mode">Mode</Label>
          <Select
            value={form.mode || "unset"}
            onValueChange={(v) => set({ mode: v })}
          >
            <SelectTrigger id="s3-mode" className="w-64" data-testid="s3-mode-select">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {MODE_OPTIONS.map((m) => (
                <SelectItem key={m.value} value={m.value}>
                  {m.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-xs text-muted-foreground">
            unset disables remote uploads; saving replaces the entire configuration
            (PUT semantics).
          </p>
        </div>

        {isRustfs ? (
          <>
            <div
              data-testid="s3-honesty-note"
              className="rounded-md border border-amber-500/40 bg-amber-500/5 p-3 text-xs text-amber-800 dark:text-amber-300"
            >
              {RUSTFS_HONESTY_NOTE}
            </div>
            <p className="text-xs text-muted-foreground">
              RustFS is platform-managed: endpoint, bucket and credentials are derived
              automatically (endpoint <code>http://rustfs:9000</code>, single shared
              bucket on the internal network).
            </p>
          </>
        ) : null}

        {isExternal ? (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="s3-endpoint">Endpoint URL</Label>
              <Input
                id="s3-endpoint"
                placeholder="https://s3.amazonaws.com"
                className="font-mono text-xs"
                value={form.endpointUrl}
                onChange={(e) => set({ endpointUrl: e.target.value })}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="s3-region">Region</Label>
              <Input
                id="s3-region"
                placeholder="us-east-1"
                className="font-mono text-xs"
                value={form.region}
                onChange={(e) => set({ region: e.target.value })}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="s3-bucket">Bucket</Label>
              <Input
                id="s3-bucket"
                className="font-mono text-xs"
                value={form.bucket}
                onChange={(e) => set({ bucket: e.target.value })}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="s3-access-key">Access key ID</Label>
              <Input
                id="s3-access-key"
                className="font-mono text-xs"
                value={form.accessKeyId}
                onChange={(e) => set({ accessKeyId: e.target.value })}
              />
            </div>
            <div className="space-y-1.5 sm:col-span-2">
              <Label htmlFor="s3-secret">Secret access key</Label>
              <Input
                id="s3-secret"
                type="password"
                autoComplete="off"
                className="font-mono text-xs"
                placeholder={
                  stored?.secret_fingerprint
                    ? `stored (fingerprint ${stored.secret_fingerprint}) — write-only, never read back`
                    : "write-only, never read back"
                }
                value={form.secret}
                onChange={(e) => set({ secret: e.target.value })}
              />
              <p className="text-xs text-muted-foreground">
                {stored?.secret_fingerprint
                  ? `A secret is stored (fingerprint ${stored.secret_fingerprint}). `
                  : "No secret stored. "}
                Saving with this field blank clears the stored secret (full-replace
                semantics).
              </p>
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                data-testid="s3-path-style-toggle"
                className="h-4 w-4 accent-foreground"
                checked={form.pathStyle}
                onChange={(e) => set({ pathStyle: e.target.checked })}
              />
              Path-style addressing (self-hosted S3: MinIO/RustFS)
            </label>
          </div>
        ) : null}

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            data-testid="s3-public-toggle"
            className="h-4 w-4 accent-foreground"
            disabled={!isRustfs}
            checked={isRustfs && form.publicExposed}
            onChange={(e) => set({ publicExposed: e.target.checked })}
          />
          Expose RustFS publicly at s3.&lt;base_domain&gt; (rustfs only; requires
          base_domain)
        </label>

        {saveError ? (
          <EnvelopeAlertFrom envelope={saveError} />
        ) : null}
        {saveResult ? (
          <p className="text-xs text-emerald-600 dark:text-emerald-400">
            Settings saved (full configuration replaced).
          </p>
        ) : null}

        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            data-testid="s3-test-button"
            disabled={testMutation.isPending}
            onClick={() => {
              setSaveResult(null);
              testMutation.mutate();
            }}
          >
            Test connection
          </Button>
          <Button
            type="button"
            size="sm"
            disabled={saveMutation.isPending}
            onClick={() => {
              setTestResult(null);
              setTestError(null);
              saveMutation.mutate();
            }}
          >
            {saveMutation.isPending ? "Saving…" : "Save"}
          </Button>
        </div>

        {testError ? (
          <EnvelopeAlert
            code={testError.code}
            message={testError.message}
            suggestion={testError.suggestion}
            docs={testError.docs}
          />
        ) : null}
        {testResult ? (
          <div
            data-testid="s3-test-result"
            className="space-y-2 rounded-md border p-3"
          >
            <div className="flex items-center gap-2 text-xs font-medium">
              <span
                aria-hidden
                className={`h-2 w-2 rounded-full ${
                  testResult.ok ? "bg-emerald-500" : "bg-red-500"
                }`}
              />
              {testResult.ok ? "Connection OK" : `Connection failed${testResult.failed_step ? ` at step ${testResult.failed_step}` : ""}`}
              <code className="text-muted-foreground">{testResult.endpoint_url}</code>
            </div>
            <div className="space-y-1">
              {(testResult.steps ?? []).map((s) => (
                <ProbeStepRow key={s.step} step={s} />
              ))}
            </div>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
