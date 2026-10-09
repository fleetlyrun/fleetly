import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { RocketIcon } from "lucide-react";
import { toast } from "sonner";
import type { components } from "@/api/delivery";
import { apiSend } from "@/api/client";
import { buildDeployPayload, type DeployFormState } from "@/lib/deployPayload";
import { useUploads, type AppEntry } from "@/lib/catalog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { describeError } from "@/lib/api-errors";

// DeploySheet（List 原型的主操作，UI v2 批 2）：四源部署表单的 Sheet 形态
// ——image 直投 / compose 受控子集 / spec_file 裸 AppSpec / upload 产物。
// 载荷构造单源仍是 lib/deployPayload（纯函数已测，语义保真）；策略声明
// 走 compose/spec 扩展键（ADR-0048 决策 6），不设独立控件。
// 成功反馈走 toast 带动作链接（"View"跳详情）——MutationBanner 退役。

type DeployResponse = {
  deployment?: components["schemas"]["v1Deployment"];
  admission?: components["schemas"]["v1Admission"];
};

type SourceKind = DeployFormState["source"];

const SOURCE_HINTS: Record<SourceKind, string> = {
  image: "Single-process image deploy (rolling strategy by default — per-process strategy needs compose or spec file).",
  compose: "Compose YAML (controlled subset). Per-process strategy: deploy.strategy: blue-green under a service.",
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
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  apps: AppEntry[];
  defaultAppId: string;
  projectId: string;
  /** 部署受理后的跳转回调（路由语境里给 navigate） */
  onDeployed?: (deploymentId: string) => void;
}) {
  const queryClient = useQueryClient();
  const [appId, setAppId] = useState(defaultAppId);
  const [source, setSource] = useState<SourceKind>("image");
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
  const uploads = useUploads(selectedApp?.project_id ?? projectId);
  const uploadRows = uploads.data ?? [];

  const mutation = useMutation<DeployResponse, Error, void>({
    mutationFn: () =>
      apiSend<DeployResponse>(
        "/v1/deployments",
        "POST",
        buildDeployPayload({
          appId: effectiveAppId,
          source,
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
        }),
      ),
    onSuccess: (response) => {
      queryClient.invalidateQueries({ queryKey: ["deployments"] });
      const id = response.deployment?.id;
      toast("Deployment created", {
        description: id ? `${id.slice(0, 12)}… queued` : undefined,
        action: id
          ? { label: "View", onClick: () => onDeployed?.(id) }
          : undefined,
      });
      onOpenChange(false);
    },
  });

  const canSubmit =
    !mutation.isPending && effectiveAppId !== "" && (source !== "upload" || uploadId !== "");

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-[540px] flex-col gap-0 sm:max-w-[540px]">
        <SheetHeader className="pb-0">
          <SheetTitle className="flex items-center gap-2">
            <RocketIcon className="size-4" />
            Deploy
          </SheetTitle>
          <SheetDescription>Create a deployment — the server queues and supersedes by generation.</SheetDescription>
        </SheetHeader>
        <div className="flex flex-1 flex-col gap-4 overflow-y-auto px-4 py-4">
          <div className="flex flex-col gap-2">
            <Label>App</Label>
            <Select value={effectiveAppId} onValueChange={setAppId}>
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

          <div className="flex flex-col gap-2">
            <Label>Source</Label>
            <Select value={source} onValueChange={(value) => setSource(value as SourceKind)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="image">Image</SelectItem>
                <SelectItem value="compose">Compose YAML</SelectItem>
                <SelectItem value="spec">Spec file (AppSpec JSON)</SelectItem>
                <SelectItem value="upload">Uploaded source</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-[11.5px] text-muted-foreground">{SOURCE_HINTS[source]}</p>
          </div>

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
            <FieldRow label="Compose YAML" hint="x-fleetly-first-boot-jobs and deploy.strategy extension keys are honored">
              <Textarea rows={10} value={composeYaml} onChange={(event) => setComposeYaml(event.target.value)} spellCheck={false} />
            </FieldRow>
          ) : null}

          {source === "spec" ? (
            <FieldRow label="AppSpec (protojson)" hint="snake_case; app ref is overwritten from the selected app">
              <Textarea rows={12} value={specJson} onChange={(event) => setSpecJson(event.target.value)} spellCheck={false} />
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

          <Separator />

          <div className="grid grid-cols-2 gap-3">
            <FieldRow label="Idempotency key" hint="same key replays the original response">
              <Input value={idempotencyKey} onChange={(event) => setIdempotencyKey(event.target.value)} placeholder="deploy-1" />
            </FieldRow>
            <FieldRow label="Commit SHA" hint="dedupes webhook + manual submits">
              <Input value={commitSha} onChange={(event) => setCommitSha(event.target.value)} />
            </FieldRow>
          </div>
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <Switch checked={supersede} onCheckedChange={setSupersede} />
            Supersede in-flight deployments (take over the queue)
          </label>

          {mutation.isError ? (
            <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2">
              <div className="text-xs font-semibold text-[var(--status-danger)]">{describeError(mutation.error).title}</div>
              <div className="mt-0.5 font-mono text-[11px] break-all text-muted-foreground">
                {describeError(mutation.error).detail}
              </div>
            </div>
          ) : null}
        </div>
        <div className="flex justify-end gap-2 border-t px-4 py-3">
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button disabled={!canSubmit} onClick={() => void mutation.mutate()}>
            {mutation.isPending ? "Submitting…" : "Deploy"}
          </Button>
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

function FieldRow({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label className="text-xs">{label}</Label>
      {children}
      {hint ? <p className="text-[11px] leading-snug text-muted-foreground">{hint}</p> : null}
    </div>
  );
}
