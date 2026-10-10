import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import type { components } from "../api/delivery";
import { apiSend } from "../api/client";
import { useUploads, type AppEntry } from "../lib/catalog";
import { buildDeployPayload, type DeployFormState } from "../lib/deployPayload";
import { ErrorNote, Field, Modal, PrimaryButton, Select, TextArea, TextInput } from "./ui";

// DeployForm 是四源部署表单（F3.1 写面核心）：image 直投 / compose_yaml
// 受控子集 / spec_file 裸 AppSpec / upload_id 上传产物——互斥由服务端
// 执法（DeployRequest 契约），UI 面按源切换字段组。strategy 声明面在
// compose 的 deploy.strategy 扩展键与 spec_file 的 processes[].strategy
//（ADR-0048 决策 6：DeployRequest 不加 strategy 旗标）——表单内提示词
// 引导，不设独立控件（诚实面：直投形态无策略通道）。载荷构造单源在
// lib/deployPayload.ts（纯函数，消费面单测锚）。

type DeployResponse = { deployment?: components["schemas"]["v1Deployment"]; admission?: components["schemas"]["v1Admission"] };

type SourceKind = DeployFormState["source"];

const SOURCE_HINTS: Record<SourceKind, string> = {
 image:"Single-process image deploy (rolling strategy by default — per-process strategy needs compose or spec file).",
 compose:"Compose YAML (controlled subset). Per-process strategy: deploy.strategy: blue-green under a service.",
 spec:"Bare AppSpec in protojson (snake_case). Full spec surface incl. processes[].strategy.",
 upload:"Build from a previously uploaded source directory (uploads tab creates them).",
};

export function DeployForm({
 open,
 onClose,
 apps,
 defaultAppId,
 onDeployed,
}: {
 open: boolean;
 onClose: () => void;
 apps: AppEntry[];
 defaultAppId: string;
 onDeployed?: (deploymentId: string) => void;
}) {
 const [appId, setAppId] = useState(defaultAppId);
 const [source, setSource] = useState<SourceKind>("image");
 const [image, setImage] = useState("nginx:1.27");
 const [processName, setProcessName] = useState("");
 const [port, setPort] = useState("");
 const [protocol, setProtocol] = useState("http");
 const [envText, setEnvText] = useState("");
 const [httpProbe, setHttpProbe] = useState("");
 const [tcpProbe, setTcpProbe] = useState("");
 const [composeYaml, setComposeYaml] = useState("services:\n web:\n image: nginx:1.27\n");
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

 const selectedApp = apps.find((app) => app.id === appId);
 const uploads = useUploads(selectedApp?.project_id ??"");
 const uploadRows = uploads.data ?? [];

 const mutation = useMutation<DeployResponse, Error, void>({
 mutationFn: () => apiSend<DeployResponse>("/v1/deployments","POST", buildDeployPayload({
 appId,
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
    })),
 onSuccess: (response) => {
 const id = response.deployment?.id;
 if (id && onDeployed) onDeployed(id);
 onClose();
    },
  });

 return (
    <Modal title="Deploy"open={open} onClose={onClose}>
      <div className="flex flex-col gap-3">
        <Field label="App">
          <Select value={appId} onChange={(event) => setAppId(event.target.value)}>
            {apps.map((app) => (
              <option key={app.id} value={app.id}>
                {app.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Source"hint={SOURCE_HINTS[source]}>
          <Select value={source} onChange={(event) => setSource(event.target.value as SourceKind)}>
            <option value="image">Image</option>
            <option value="compose">Compose YAML</option>
            <option value="spec">Spec file (AppSpec JSON)</option>
            <option value="upload">Uploaded source</option>
          </Select>
        </Field>

        {source ==="image"? (
          <>
            <Field label="Image">
              <TextInput value={image} onChange={(event) => setImage(event.target.value)} placeholder="nginx:1.27"/>
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Process name"hint="default: web">
                <TextInput value={processName} onChange={(event) => setProcessName(event.target.value)} placeholder="web"/>
              </Field>
              <Field label="Port"hint="Route-facing; attaches the project default network">
                <TextInput value={port} onChange={(event) => setPort(event.target.value)} placeholder="8080"inputMode="numeric"/>
              </Field>
            </div>
            {port !==""? (
              <Field label="Protocol">
                <Select value={protocol} onChange={(event) => setProtocol(event.target.value)}>
                  <option value="http">http</option>
                  <option value="h2c">h2c</option>
                  <option value="tcp">tcp</option>
                </Select>
              </Field>
            ) : null}
            <Field label="Env"hint="one KEY=VALUE per line (App-level env; overrides project shared variables)">
              <TextArea rows={3} value={envText} onChange={(event) => setEnvText(event.target.value)} placeholder="LOG_LEVEL=info"/>
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="HTTP probe"hint="readiness path, e.g. /healthz">
                <TextInput value={httpProbe} onChange={(event) => setHttpProbe(event.target.value)} placeholder="/healthz"/>
              </Field>
              <Field label="TCP probe"hint="readiness port">
                <TextInput value={tcpProbe} onChange={(event) => setTcpProbe(event.target.value)} placeholder="5432"inputMode="numeric"/>
              </Field>
            </div>
          </>
        ) : null}

        {source ==="compose"? (
          <Field label="Compose YAML"hint="x-fleetly-first-boot-jobs and deploy.strategy extension keys are honored">
            <TextArea rows={10} value={composeYaml} onChange={(event) => setComposeYaml(event.target.value)} spellCheck={false} />
          </Field>
        ) : null}

        {source ==="spec"? (
          <Field label="AppSpec (protojson)"hint="snake_case; app ref is overwritten from the selected app">
            <TextArea rows={12} value={specJson} onChange={(event) => setSpecJson(event.target.value)} spellCheck={false} />
          </Field>
        ) : null}

        {source ==="upload"? (
          <>
            <Field label="Upload"hint="create sources on the Resources → Uploads tab">
              <Select value={uploadId} onChange={(event) => setUploadId(event.target.value)}>
                <option value="">select an upload…</option>
                {uploadRows.map((upload) => (
                  <option key={upload.id} value={upload.id}>
                    {upload.id?.slice(0, 10)}… · {(Number(upload.size_bytes ?? 0) / 1024).toFixed(1)} KiB · {upload.digest?.slice(0, 12)}…
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Builder">
              <Select value={builder} onChange={(event) => setBuilder(event.target.value)}>
                <option value="dockerfile">dockerfile</option>
                <option value="railpack">railpack</option>
                <option value="static">static</option>
              </Select>
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Dockerfile path"hint="tar-relative; default Dockerfile">
                <TextInput value={dockerfile} onChange={(event) => setDockerfile(event.target.value)} placeholder="Dockerfile"/>
              </Field>
              {builder ==="railpack"? (
                <Field label="Railpack version"hint="must equal the platform pin">
                  <TextInput value={railpackVersion} onChange={(event) => setRailpackVersion(event.target.value)} placeholder="0.39.0"/>
                </Field>
              ) : null}
              {builder ==="static"? (
                <Field label="Output dir"hint='static builder artifact dir; default"."'>
                  <TextInput value={outputDir} onChange={(event) => setOutputDir(event.target.value)} placeholder="."/>
                </Field>
              ) : null}
            </div>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Process name">
                <TextInput value={processName} onChange={(event) => setProcessName(event.target.value)} placeholder="web"/>
              </Field>
              <Field label="Port"hint="served port for Route resolution">
                <TextInput value={port} onChange={(event) => setPort(event.target.value)} placeholder="8080"inputMode="numeric"/>
              </Field>
            </div>
            {port !==""? (
              <Field label="Protocol">
                <Select value={protocol} onChange={(event) => setProtocol(event.target.value)}>
                  <option value="http">http</option>
                  <option value="h2c">h2c</option>
                  <option value="tcp">tcp</option>
                </Select>
              </Field>
            ) : null}
          </>
        ) : null}

        <div className="grid grid-cols-2 gap-3">
          <Field label="Idempotency key"hint="optional; same key replays the original response">
            <TextInput value={idempotencyKey} onChange={(event) => setIdempotencyKey(event.target.value)} placeholder="deploy-1"/>
          </Field>
          <Field label="Commit SHA"hint="optional; dedupes webhook + manual submits">
            <TextInput value={commitSha} onChange={(event) => setCommitSha(event.target.value)} placeholder=""/>
          </Field>
        </div>
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          <input type="checkbox"checked={supersede} onChange={(event) => setSupersede(event.target.checked)} />
          Supersede in-flight deployments (take over the queue)
        </label>

        {mutation.isError ? <ErrorNote error={mutation.error} hint="POST /v1/deployments was rejected — see the envelope above."/> : null}

        <div className="flex justify-end gap-2">
          <PrimaryButton
 disabled={mutation.isPending || appId ===""|| (source ==="upload"&& uploadId ==="")}
 onClick={() => void mutation.mutate()}
          >
            {mutation.isPending ?"Submitting…":"Deploy"}
          </PrimaryButton>
        </div>
      </div>
    </Modal>
  );
}
