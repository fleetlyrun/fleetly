// DeployRequest 载荷构造（DeployForm 的纯函数面——四源互斥的客户端
// 形态在此单源，服务端执法是契约真源，本函数只负责"把表单状态翻译成
// 诚实载荷"：空值不发送（undefined 字段 protojson 缺席）。

export interface DeployFormState {
  appId: string;
  source: "image" | "compose" | "spec" | "upload";
  image: string;
  processName: string;
  port: string;
  protocol: string;
  envText: string;
  httpProbe: string;
  tcpProbe: string;
  composeYaml: string;
  specJson: string;
  uploadId: string;
  builder: string;
  dockerfile: string;
  railpackVersion: string;
  outputDir: string;
  idempotencyKey: string;
  commitSha: string;
  supersede: boolean;
}

// envLines 文本域 → map（K=V 行；空行/注释行跳过；无 = 的行跳过）。
export function envMapOf(text: string): Record<string, string> {
  const map: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (trimmed === "" || trimmed.startsWith("#")) continue;
    const eq = trimmed.indexOf("=");
    if (eq > 0) map[trimmed.slice(0, eq)] = trimmed.slice(eq + 1);
  }
  return map;
}

// buildDeployPayload 按源形态组载荷（int 字段 Number 化；协议与探针仅
// 在声明面出现——port 空则 protocol 不发送，与"protocol is only
// meaningful together with a declared port"的服务端执法对齐）。undefined
// 键在出口前剥除（"空值不发送"是字面契约——键缺席而非键在值空）。
export function buildDeployPayload(state: DeployFormState): Record<string, unknown> {
  const envMap = envMapOf(state.envText);
  const payload: Record<string, unknown> = {
    app_id: state.appId,
    idempotency_key: state.idempotencyKey || undefined,
    commit_sha: state.commitSha || undefined,
    supersede: state.supersede || undefined,
    ...(state.source === "image"
      ? {
          image: state.image,
          process_name: state.processName || undefined,
          port: state.port === "" ? undefined : Number(state.port),
          protocol: state.port === "" ? undefined : state.protocol,
          env: Object.keys(envMap).length === 0 ? undefined : envMap,
          http_probe: state.httpProbe || undefined,
          tcp_probe: state.tcpProbe === "" ? undefined : Number(state.tcpProbe),
        }
      : {}),
    ...(state.source === "compose" ? { compose_yaml: state.composeYaml } : {}),
    ...(state.source === "spec" ? { spec_file: state.specJson } : {}),
    ...(state.source === "upload"
      ? {
          upload_id: state.uploadId,
          builder: state.builder === "dockerfile" ? undefined : state.builder,
          dockerfile: state.dockerfile || undefined,
          railpack_version: state.railpackVersion || undefined,
          output_dir: state.outputDir || undefined,
          process_name: state.processName || undefined,
          port: state.port === "" ? undefined : Number(state.port),
          protocol: state.port === "" ? undefined : state.protocol,
        }
      : {}),
  };
  return Object.fromEntries(Object.entries(payload).filter(([, value]) => value !== undefined));
}
