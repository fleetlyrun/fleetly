// dokploy 导出解析的 TypeScript 移植（C3：竞品迁移钩子的 Console 半边）。
// 真源 = internal/spec/dokploy.go（F3.3，ADR-0050 决策 5）——本移植是
// 叶子拷贝，语义由 dokploy.test.ts 用 CLI golden 同款夹具钉死（parity
// 断言：同一份导出，两侧计划一致）。映射面 fail-closed：compose 插值孔
// 未消解即整条 skip，不静默落字面 "${...}" 值。

export interface DokployApplication {
  applicationId?: string;
  name?: string;
  buildType?: string;
  dockerImage?: string;
  env?: string;
  sourceType?: string;
  repository?: string;
}

export interface DokployCompose {
  composeId?: string;
  name?: string;
  composeContent?: string;
  env?: string;
}

export interface DokployDomain {
  domainId?: string;
  host?: string;
  port?: number;
  applicationId?: string;
  composeId?: string;
  serviceName?: string;
}

export interface DokployDatabase {
  databaseId?: string;
  name?: string;
  type?: string;
}

export interface DokployExport {
  applications?: DokployApplication[];
  compose?: DokployCompose[];
  domains?: DokployDomain[];
  databases?: DokployDatabase[];
}

export interface DokployRoutePlan {
  host: string;
  process: string;
  port: number;
}

export interface DokployAppPlan {
  name: string;
  image: string;
  composeYaml: string;
  env: Record<string, string>;
  routes: DokployRoutePlan[];
}

export interface DokployDatabasePlan {
  name: string;
  engine: string;
}

export interface DokploySkip {
  kind: string;
  item: string;
  reason: string;
}

export interface DokployPlan {
  apps: DokployAppPlan[];
  databases: DokployDatabasePlan[];
  skipped: DokploySkip[];
}

// dokployEngineMap 是引擎词面映射（值域外的显式 skip；引擎在册性由
// CreateDatabase 受理位单源执法）。
const ENGINE_MAP: Record<string, string> = {
  postgres: "postgres",
  mysql: "mysql",
  mongo: "mongo",
  redis: "redis",
};

// parseDokploy 解析导出文档为迁移计划（与 Go ParseDokploy 逐步同构：
// applications → compose → domains 挂靠 → databases；skip 序即解析序）。
export function parseDokploy(body: string): DokployPlan {
  let ex: DokployExport;
  try {
    ex = JSON.parse(body) as DokployExport;
  } catch (cause) {
    throw new Error(`dokploy: export is not valid JSON: ${(cause as Error).message}`);
  }
  const plan: DokployPlan = { apps: [], databases: [], skipped: [] };
  const appByID = new Map<string, number>();
  for (const a of ex.applications ?? []) {
    if ((a.name ?? "").trim() === "") {
      plan.skipped.push({
        kind: "application",
        item: a.applicationId ?? "",
        reason: "carries no name; dokploy exports of draft rows cannot be mapped",
      });
      continue;
    }
    if (a.buildType !== "dockerimage" || (a.dockerImage ?? "").trim() === "") {
      plan.skipped.push({
        kind: "application",
        item: a.name ?? "",
        reason: `build type "${a.buildType}" with repository "${a.repository}" is a build-source app; re-deploy it from fleetly source intake (uploads or git push) and migrate only its configuration here`,
      });
      continue;
    }
    appByID.set(a.applicationId ?? "", plan.apps.length);
    plan.apps.push({
      name: a.name ?? "",
      image: (a.dockerImage ?? "").trim(),
      composeYaml: "",
      env: parseDokployEnv(a.env ?? ""),
      routes: [],
    });
  }
  const composeByID = new Map<string, number>();
  for (const c of ex.compose ?? []) {
    if ((c.name ?? "").trim() === "" || (c.composeContent ?? "").trim() === "") {
      plan.skipped.push({
        kind: "compose",
        item: c.name ?? "",
        reason: "carries no name or no composeContent; dokploy exports of draft rows cannot be mapped",
      });
      continue;
    }
    const env = parseDokployEnv(c.env ?? "");
    const { text: content, unresolved } = substituteDokployVars(c.composeContent ?? "", env);
    if (unresolved.length > 0) {
      unresolved.sort();
      plan.skipped.push({
        kind: "compose",
        item: c.name ?? "",
        reason: `compose references variables absent from the dokploy env panel (${unresolved.join(", ")}); fleetly compose intake lands unresolved holes as literal values, so this app is skipped rather than silently corrupted`,
      });
      continue;
    }
    composeByID.set(c.composeId ?? "", plan.apps.length);
    plan.apps.push({ name: c.name ?? "", image: "", composeYaml: content, env, routes: [] });
  }
  for (const d of ex.domains ?? []) {
    let idx = -1;
    if (d.applicationId != null && d.applicationId !== "") {
      const hit = appByID.get(d.applicationId);
      if (hit !== undefined) idx = hit;
    } else if (d.composeId != null && d.composeId !== "") {
      const hit = composeByID.get(d.composeId);
      if (hit !== undefined) idx = hit;
    }
    if (idx < 0) {
      const anchor = d.applicationId || d.composeId || d.domainId || "";
      plan.skipped.push({
        kind: "domain",
        item: d.host ?? "",
        reason: `attaches to resource ${anchor} which is itself unmapped (see its skip entry)`,
      });
      continue;
    }
    plan.apps[idx].routes.push({
      host: d.host ?? "",
      process: d.serviceName && d.serviceName !== "" ? d.serviceName : "web",
      port: d.port ?? 0,
    });
  }
  for (const db of ex.databases ?? []) {
    const engine = ENGINE_MAP[(db.type ?? "").trim().toLowerCase()];
    if (engine === undefined) {
      plan.skipped.push({
        kind: "database",
        item: db.name ?? "",
        reason: `dokploy engine "${db.type}" has no fleetly mapping (engines: postgres, mysql, mongo, redis)`,
      });
      continue;
    }
    plan.databases.push({ name: db.name ?? "", engine });
  }
  return plan;
}

// parseDokployEnv 解析 raw env 文本（KEY=VALUE 行；注释/空行跳过）。
function parseDokployEnv(raw: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const lineRaw of raw.split("\n")) {
    const line = lineRaw.trim();
    if (line === "" || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    out[line.slice(0, eq).trim()] = line.slice(eq + 1);
  }
  return out;
}

// substituteDokployVars 把 env 面替换进 compose 文本的 ${KEY} 孔，返回
// 替换后文本与未消解键集（fail-closed 消费）。
function substituteDokployVars(content: string, env: Record<string, string>): { text: string; unresolved: string[] } {
  if (!content.includes("${")) return { text: content, unresolved: [] };
  const unresolved: string[] = [];
  const seen = new Set<string>();
  const text = content.replace(/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/g, (match, key: string) => {
    if (key in env) return env[key];
    if (!seen.has(key)) {
      seen.add(key);
      unresolved.push(key);
    }
    return match;
  });
  return { text, unresolved };
}
