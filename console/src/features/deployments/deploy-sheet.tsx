import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCollection, useCommand } from "@lynx-go/uikit-core";
import type { ApiError, CommandResult } from "@lynx-go/uikit-core/kernel";
import { RocketIcon } from "lucide-react";
import { toast } from "sonner";
import { ApiError as FetchApiError, apiFetch } from "@/api/client";
import type { DeployFormState } from "@/lib/deployPayload";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { AppEntry } from "@/kit/apps";
import { uploadsResource, uploadsLookup } from "@/kit/storage";
import { deploymentsResource } from "@/kit/deployments";
import { describeApiError } from "@/kit/errors";

// DeploySheet（List 原型的主操作，UI v2 批 2）：五源部署表单的 Sheet 形态
// ——current（冻结体可编辑）/ image 直投 / compose 受理语言 / spec_file 裸
// AppSpec / upload 产物 / hook 托管（Deploy now 现拉）。
// 载荷构造单源仍是 lib/deployPayload（纯函数已测，语义保真）；策略声明
// 走 compose/spec 扩展键（ADR-0048 决策 6），不设独立控件。
// 批2（2026-10-26）提交面命令化：**保留壳只换提交面**——五源动态表单不迁
// Form 块，deploy/hookRefresh 走 deployments resource commands；成功反馈
// 走 toast 带动作链接（"View"跳详情）。结果通道（rejected/conflict）以
// message-first 结果盒呈现（form 块结果分支同形）——spec_file 链携带
// AppSpec.restart_seq（ADR-0083 OCC 面），陈旧序数的服务端 CAS 拒绝落
// conflict 分支，如实呈现不复现为 ok；失败通道（网络/401/5xx）走
// describeApiError 文案盒。

type SourceKind = DeployFormState["source"];

const SOURCE_HINTS: Record<SourceKind, string> = {
  current: "The app's frozen spec, prefilled and editable (deploys as spec_file; hook-owned apps deploy a runtime overlay).",
  image: "Single-process image deploy (rolling strategy by default — per-process strategy needs compose or spec file).",
  compose: "Compose YAML (compose-spec syntax, x-fleetly extensions). Per-process strategy: deploy.x-fleetly-strategy: blue-green under a service.",
  spec: "Bare AppSpec in protojson (snake_case). Full spec surface incl. processes[].strategy.",
  upload: "Build from a previously uploaded source directory (Data → Uploads creates them).",
};

export function DeploySheet({
  open,
  onOpenChange,
  apps,
  defaultAppId,
  projectId,
  onDeployed,
  from,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 目录行集由宿主路由注入（kit useCollection 的大页行集，只读） */
  apps: readonly AppEntry[];
  defaultAppId: string;
  projectId: string;
  /** 部署受理后的跳转回调（路由语境里给 navigate） */
  onDeployed?: (deploymentId: string) => void;
  /** 宿主路由 id（uploads 读面的 useCollection from 锚——批4 目录读收编） */
  from: string;
}) {
  const [appId, setAppId] = useState(defaultAppId);
  // 缺省源推导（ADR-0072 写面）：已有冻结 Spec 的 App 缺省走 current
  // （预填可编辑）；显式选择后以选择为准。
  const [sourceChosen, setSourceChosen] = useState<SourceKind | null>(null);
  const [image, setImage] = useState("nginx:1.27");
  const [processName, setProcessName] = useState("");
  const [port, setPort] = useState("");
  const [protocol, setProtocol] = useState("http");
  const [envText, setEnvText] = useState("");
  const [httpProbe, setHttpProbe] = useState("");
  const [tcpProbe, setTcpProbe] = useState("");
  const [composeYaml, setComposeYaml] = useState("services:\n  web:\n    image: nginx:1.27\n");
  const [specJson, setSpecJson] = useState(
    '{"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27"}]}',
  );
  const [uploadId, setUploadId] = useState("");
  const [builder, setBuilder] = useState("dockerfile");
  const [dockerfile, setDockerfile] = useState("");
  const [railpackVersion, setRailpackVersion] = useState("");
  const [outputDir, setOutputDir] = useState("");
  const [idempotencyKey, setIdempotencyKey] = useState("");
  const [commitSha, setCommitSha] = useState("");
  const [supersede, setSupersede] = useState(false);

  const effectiveAppId = appId || defaultAppId;
  const selectedApp = apps.find((app) => app.id === effectiveAppId);
  // 批4（2026-10-27）：uploads 目录读面迁 kit useCollection（15s 轮询同旧
  // 拍；fetch 对空 projectId 自守为空段——旧 enabled 门同语义）。
  const uploads = useCollection(uploadsResource, {
    from,
    search: uploadsLookup,
    scope: { projectId: selectedApp?.project_id ?? projectId },
    query: { refetchInterval: 15_000 },
  });
  const uploadRows = uploads.rows;

  // 生效锚 + 冻结体预填（GetAppSpec 的 Revision 锚，ADR-0071）。
  const frozen = useQuery({
    queryKey: ["resources", "app-spec-sheet", effectiveAppId],
    enabled: open && effectiveAppId !== "",
    queryFn: async () => {
      try {
        return await apiFetch<{ spec?: unknown; revision_seq?: string }>(`/v1/apps/${encodeURIComponent(effectiveAppId)}/spec`);
      } catch (error) {
        if (error instanceof FetchApiError && error.status === 404) return null;
        throw error;
      }
    },
  });
  const hasFrozen = frozen.data != null;
  const queryClient = useQueryClient();

  // hook 所有权（ADR-0072）：hook 在即整张四源表单退场（Deploy from git
  // 分支接管），source 归 null 让各源条件段全部跳过。
  const hook = useQuery({
    queryKey: ["resources", "app-hook", effectiveAppId],
    enabled: open && effectiveAppId !== "",
    queryFn: async () => {
      try {
        const res = await apiFetch<{ hook?: { repo?: string; branch?: string; dockerfile?: string } }>(
          `/v1/apps/${encodeURIComponent(effectiveAppId)}/hook`);
        return res?.hook ?? null;
      } catch (error) {
        if (error instanceof FetchApiError && error.status === 404) return null;
        throw error;
      }
    },
  });
  const hookOwned = hook.data != null;
  const source: SourceKind | null = hookOwned ? null : (sourceChosen ?? (hasFrozen ? "current" : "image"));

  // 拉取通道（RefreshGitHook）：hook 托管 App 的部署主操作——不依赖
  // GitHub 侧 webhook,HEAD 现解析;admission 同链（同 HEAD deduplicated）。
  const refresh = useCommand(deploymentsResource.commands.hookRefresh, {
    scope: { projectId },
    // rejected/conflict/failure → 下方结果盒（盒子优先，toast 不双响）
    onSettled: (result) => {
      if (result?.status === "ok") {
        const suffix =
          result.payload.outcome === "deduplicated" ? " (deduplicated — same commit already in the pipeline)" : "";
        toast(`Deployment ${result.payload.deploymentId ?? "?"} queued @ ${result.payload.commit?.slice(0, 7) ?? "?"}${suffix}`, {
          action: result.payload.deploymentId
            ? { label: "View", onClick: () => onDeployed?.(result.payload.deploymentId!) }
            : undefined,
        });
        onOpenChange(false);
      }
    },
  });

  // current 源预填：冻结体 pretty JSON（App 切换/冻结更新时重播，除非已
  // 手编）。
  const [specEdited, setSpecEdited] = useState(false);
  useEffect(() => {
    if (source === "current" && !specEdited && frozen.data?.spec != null) {
      setSpecJson(JSON.stringify(frozen.data.spec, null, 2));
    }
  }, [source, specEdited, frozen.data]);

  // 提交面（批2 命令化）：五源表单状态整包交给 deployments.deploy 命令
  // （载荷构造单源 lib/deployPayload 在命令内消费）；失效面 = 默认同资源
  // + legacyKeys 桥 ["deployments"]（与旧 invalidate 键同形）。
  const deploy = useCommand(deploymentsResource.commands.deploy, {
    scope: { projectId },
    // rejected/conflict/failure → 下方结果盒（盒子优先，toast 不双响）
    onSettled: (result) => {
      if (result?.status === "ok") {
        const id = result.payload.deployment?.id;
        toast("Deployment created", {
          description: id ? `${id.slice(0, 12)}… queued` : undefined,
          action: id
            ? { label: "View", onClick: () => onDeployed?.(id) }
            : undefined,
        });
        onOpenChange(false);
      }
    },
  });

  // OCC conflict 的「刷新重载」（torchwood B2-5 同款 UX）：spec 链冲突 =
  // 冻结体已被其他部署推进（restart_seq CAS）或幂等键冲突——失效冻结体
  // 查询回服务端态、清除手编旗标让预填重播、清结果盒。仅在 spec 链源
  //（current/spec）出现 conflict 时呈现。
  const reloadFrozenSpec = () => {
    void queryClient.invalidateQueries({ queryKey: ["resources", "app-spec-sheet", effectiveAppId] });
    setSpecEdited(false);
    deploy.reset();
  };
  const showConflictReload =
    deploy.result?.status === "conflict" && (source === "current" || source === "spec");

  const specJsonValid = (() => {
    if (source !== "spec" && source !== "current") return true;
    try {
      JSON.parse(specJson);
      return true;
    } catch {
      return false;
    }
  })();
  const canSubmit =
    !deploy.pending &&
    effectiveAppId !== "" &&
    specJsonValid &&
    (source !== "upload" || uploadId !== "");

  // 重开清上一轮结果/失败面（结果盒不跨会话残留）
  const resetDeploy = deploy.reset;
  const resetRefresh = refresh.reset;
  useEffect(() => {
    if (open) {
      resetDeploy();
      resetRefresh();
    }
  }, [open, resetDeploy, resetRefresh]);

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-[540px] flex-col gap-0 sm:max-w-[540px]">
        <SheetHeader className="pb-0">
          <SheetTitle className="flex items-center gap-2">
            <RocketIcon className="size-4" />
            Deploy
          </SheetTitle>
          <SheetDescription>
            {hookOwned
              ? "Deploy from git — pulls the branch HEAD now; pushes trigger automatically once the webhook is configured."
              : "Create a deployment — the server queues and supersedes by generation."}
          </SheetDescription>
        </SheetHeader>
        <div className="flex flex-1 flex-col gap-4 overflow-y-auto px-4 py-4">
          <div className="flex flex-col gap-2">
            <Label>
              App
              {frozen.data?.revision_seq ? (
                <span className="ml-2 font-mono text-2xs font-normal text-muted">
                  frozen R{frozen.data.revision_seq}
                </span>
              ) : null}
            </Label>
            <Select
              value={effectiveAppId}
              onValueChange={(value) => {
                setAppId(value);
                setSpecEdited(false);
              }}>
              <SelectTrigger>
                <SelectValue placeholder="select an app…" />
              </SelectTrigger>
              <SelectContent>
                {apps.map((app) => (
                  <SelectItem key={app.id} value={app.id}>
                    {app.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {hookOwned ? (
            <div className="flex flex-col gap-2">
              <Label>Git material</Label>
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
                <dt className="text-muted">repo</dt>
                <dd className="break-all font-mono">{hook.data?.repo ?? "—"}</dd>
                <dt className="text-muted">branch</dt>
                <dd className="font-mono">{hook.data?.branch || "every branch"}</dd>
                <dt className="text-muted">compose / dockerfile</dt>
                <dd className="break-all font-mono">{hook.data?.dockerfile || "Dockerfile"}</dd>
              </dl>
              <p className="text-2xs leading-snug text-muted">
                <span className="font-semibold text-fg">Deploy now</span> pulls the branch HEAD and deploys immediately —
                no webhook required. A <span className="font-mono">.yml/.yaml</span> file becomes processes (images deploy
                directly); anything else builds as a Dockerfile. Pushes trigger automatically once the GitHub webhook is configured
                (URL on the hook card in Settings).
              </p>
              <p className="text-2xs leading-snug text-muted">
                Runtime settings live in Variables — the next pull bakes them. Material-source deploys (image/compose/upload) are
                refused while the hook owns the spec; removing the hook returns ownership to this sheet.
              </p>
            </div>
          ) : (
          <div className="flex flex-col gap-2">
            <Label>Source</Label>
            <Select
              value={source ?? undefined}
              onValueChange={(value) => {
                setSourceChosen(value as SourceKind);
                if (value !== "current") setSpecEdited(false);
              }}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {hasFrozen ? <SelectItem value="current">Current spec (edit &amp; deploy)</SelectItem> : null}
                <SelectItem value="image" disabled={hookOwned}>
                  Image
                </SelectItem>
                <SelectItem value="compose" disabled={hookOwned}>
                  Compose YAML
                </SelectItem>
                <SelectItem value="spec">Spec file (AppSpec JSON)</SelectItem>
                <SelectItem value="upload" disabled={hookOwned}>
                  Uploaded source
                </SelectItem>
              </SelectContent>
            </Select>
            <p className="text-2xs text-muted">
              {source != null ? SOURCE_HINTS[source] : ""}
              {hookOwned && (source === "current" || source === "spec")
                ? " Runtime overlay: the material half (source, build) stays with the frozen revision."
                : ""}
            </p>
          </div>
          )}

          {hookOwned ? (
            <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-info)_35%,transparent)] bg-[var(--status-info-bg)] px-3 py-2 text-2xs text-[var(--status-info)]">
              The git hook owns the material half of the spec (source, build) — pulls refresh it and keep your runtime settings
              (ADR-0072).
            </div>
          ) : null}

          {source === "image" ? (
            <>
              <FieldRow label="Image">
                <Input value={image} onChange={(event) => setImage(event.target.value)} placeholder="nginx:1.27" />
              </FieldRow>
              <div className="grid grid-cols-2 gap-3">
                <FieldRow label="Process name" hint="default: web">
                  <Input value={processName} onChange={(event) => setProcessName(event.target.value)} placeholder="web" />
                </FieldRow>
                <FieldRow label="Port" hint="Route-facing; attaches the project default network">
                  <Input value={port} onChange={(event) => setPort(event.target.value)} placeholder="8080" inputMode="numeric" />
                </FieldRow>
              </div>
              {port !== "" ? (
                <FieldRow label="Protocol">
                  <SelectiveProtocol value={protocol} onChange={setProtocol} />
                </FieldRow>
              ) : null}
              <FieldRow label="Env" hint="one KEY=VALUE per line (app-level; overrides project shared variables)">
                <Textarea rows={3} value={envText} onChange={(event) => setEnvText(event.target.value)} placeholder="LOG_LEVEL=info" />
              </FieldRow>
              <div className="grid grid-cols-2 gap-3">
                <FieldRow label="HTTP probe" hint="readiness path">
                  <Input value={httpProbe} onChange={(event) => setHttpProbe(event.target.value)} placeholder="/healthz" />
                </FieldRow>
                <FieldRow label="TCP probe" hint="readiness port">
                  <Input value={tcpProbe} onChange={(event) => setTcpProbe(event.target.value)} placeholder="5432" inputMode="numeric" />
                </FieldRow>
              </div>
            </>
          ) : null}

          {source === "compose" ? (
            <FieldRow label="Compose YAML" hint="x-fleetly-pre-deploy-jobs and deploy.x-fleetly-strategy extension keys are honored">
              <Textarea rows={10} value={composeYaml} onChange={(event) => setComposeYaml(event.target.value)} spellCheck={false} />
            </FieldRow>
          ) : null}

          {source === "spec" || source === "current" ? (
            <FieldRow
              label={source === "current" ? "Current frozen spec (editable)" : "AppSpec (protojson)"}
              hint={specJsonValid ? "snake_case; app ref is overwritten from the selected app" : "not valid JSON — fix before deploying"}
            >
              <Textarea
                rows={12}
                value={specJson}
                onChange={(event) => {
                  setSpecJson(event.target.value);
                  setSpecEdited(true);
                }}
                spellCheck={false}
              />
            </FieldRow>
          ) : null}

          {source === "upload" ? (
            <>
              <FieldRow label="Upload" hint="create sources under Data → Uploads">
                <Select value={uploadId} onValueChange={setUploadId}>
                  <SelectTrigger>
                    <SelectValue placeholder="select an upload…" />
                  </SelectTrigger>
                  <SelectContent>
                    {uploadRows.map((upload) => (
                      <SelectItem key={upload.id} value={upload.id ?? ""}>
                        {upload.id?.slice(0, 10)}… · {(Number(upload.size_bytes ?? 0) / 1024).toFixed(1)} KiB
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </FieldRow>
              <FieldRow label="Builder">
                <Select value={builder} onValueChange={setBuilder}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="dockerfile">dockerfile</SelectItem>
                    <SelectItem value="railpack">railpack</SelectItem>
                    <SelectItem value="static">static</SelectItem>
                  </SelectContent>
                </Select>
              </FieldRow>
              <div className="grid grid-cols-2 gap-3">
                <FieldRow label="Dockerfile path" hint="tar-relative; default Dockerfile">
                  <Input value={dockerfile} onChange={(event) => setDockerfile(event.target.value)} placeholder="Dockerfile" />
                </FieldRow>
                {builder === "railpack" ? (
                  <FieldRow label="Railpack version" hint="must equal the platform pin">
                    <Input value={railpackVersion} onChange={(event) => setRailpackVersion(event.target.value)} placeholder="0.39.0" />
                  </FieldRow>
                ) : null}
                {builder === "static" ? (
                  <FieldRow label="Output dir" hint='default "."'>
                    <Input value={outputDir} onChange={(event) => setOutputDir(event.target.value)} placeholder="." />
                  </FieldRow>
                ) : null}
              </div>
              <div className="grid grid-cols-2 gap-3">
                <FieldRow label="Process name">
                  <Input value={processName} onChange={(event) => setProcessName(event.target.value)} placeholder="web" />
                </FieldRow>
                <FieldRow label="Port" hint="served port for Route resolution">
                  <Input value={port} onChange={(event) => setPort(event.target.value)} placeholder="8080" inputMode="numeric" />
                </FieldRow>
              </div>
              {port !== "" ? (
                <FieldRow label="Protocol">
                  <SelectiveProtocol value={protocol} onChange={setProtocol} />
                </FieldRow>
              ) : null}
            </>
          ) : null}

          {!hookOwned ? (
          <>
          <Separator />

          <div className="grid grid-cols-2 gap-3">
            <FieldRow label="Idempotency key" hint="same key replays the original response">
              <Input value={idempotencyKey} onChange={(event) => setIdempotencyKey(event.target.value)} placeholder="deploy-1" />
            </FieldRow>
            <FieldRow label="Commit SHA" hint="dedupes webhook + manual submits">
              <Input value={commitSha} onChange={(event) => setCommitSha(event.target.value)} />
            </FieldRow>
          </div>
          <label className="flex items-center gap-2 text-xs text-muted">
            <Switch checked={supersede} onCheckedChange={setSupersede} />
            Supersede in-flight deployments (take over the queue)
          </label>
          </>
          ) : null}

          {/* 结果盒（form 块结果分支同形，Sheet 保留壳不迁 Form 块）：
              rejected 带 code 前缀；conflict（OCC：spec 链 restart_seq 陈旧
              CAS / 幂等键冲突）message-first 如实呈现 + 「刷新重载」动作槽
              （冻结体回服务端态重建草稿，torchwood B2-5 先例——resultAction
              槽已随上游 @0.3.1 回流）；失败通道走 describeApiError 文案。 */}
          <CommandResultBox
            result={deploy.result}
            failure={deploy.failure}
            action={
              showConflictReload ? (
                <Button variant="outline" size="sm" className="mt-1.5" onClick={reloadFrozenSpec}>
                  Reload frozen spec
                </Button>
              ) : undefined
            }
          />
          <CommandResultBox result={refresh.result} failure={refresh.failure} />
        </div>
        <div className="flex justify-end gap-2 border-t px-4 py-3">
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          {hookOwned ? (
            <Button disabled={refresh.pending} onClick={() => refresh.mutate({ appId: effectiveAppId })}>
              {refresh.pending ? "Pulling…" : "Deploy now"}
            </Button>
          ) : (
            <Button
              disabled={!canSubmit}
              onClick={() =>
                deploy.mutate({
                  appId: effectiveAppId,
                  source: source ?? "image",
                  image,
                  processName,
                  port,
                  protocol,
                  envText,
                  httpProbe,
                  tcpProbe,
                  composeYaml,
                  specJson,
                  uploadId,
                  builder,
                  dockerfile,
                  railpackVersion,
                  outputDir,
                  idempotencyKey,
                  commitSha,
                  supersede,
                })
              }
            >
              {deploy.pending ? "Submitting…" : "Deploy"}
            </Button>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function SelectiveProtocol({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="http">http</SelectItem>
        <SelectItem value="h2c">h2c</SelectItem>
        <SelectItem value="tcp">tcp</SelectItem>
      </SelectContent>
    </Select>
  );
}

// 提交面的 message-first 结果盒（form 块结果分支同形）：结果通道非 ok 即
// 盒（rejected 带 code 前缀）；失败通道（网络/401/5xx）走 describeApiError。
function CommandResultBox({
  result,
  failure,
  action,
}: {
  result: CommandResult<unknown> | null;
  failure: ApiError | null;
  /** 结果盒动作槽（conflict 的「刷新重载」等页面编排——form 块 resultAction 同位） */
  action?: React.ReactNode;
}) {
  if (result != null && result.status !== "ok") {
    return (
      <div
        role="alert"
        className="rounded-md border border-err/40 bg-err/10 px-3 py-2 text-xs text-err"
      >
        {result.status === "rejected" && result.code ? (
          <span className="font-mono">[{result.code}] </span>
        ) : null}
        {result.message}
        {action ? <div className="mt-1.5">{action}</div> : null}
      </div>
    );
  }
  if (failure != null) {
    return (
      <div role="alert" className="rounded-md border border-err/40 bg-err/10 px-3 py-2 font-mono text-2xs break-all text-err">
        {describeApiError(failure)}
      </div>
    );
  }
  return null;
}

function FieldRow({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label className="text-xs">{label}</Label>
      {children}
      {hint ? <p className="text-2xs leading-snug text-muted">{hint}</p> : null}
    </div>
  );
}
