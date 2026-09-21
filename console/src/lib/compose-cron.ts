// 归一化 compose 快照的 cron 服务抽取（E5 Cron，架构 §4.3）：GetRevisionSpec
// 返回 canonical JSON（compose.Spec 同构，internal/compose/spec.go——services
// 是数组，元素含 name/cron 家族字段）。console 侧只做只读投影，不做第二套
// 解析/校验（label 值契约在服务端归一化期已裁决）。

export interface CronServiceView {
  /** compose 服务名。 */
  name: string;
  /** 五段标准 crontab 式（服务端已校验）。 */
  expression: string;
  /** IANA 时区；空 = UTC（服务端缺省）。 */
  timezone?: string;
  /** 看门狗预算（time.Duration 字符串形态）；空 = 平台默认 10m。 */
  timeout?: string;
}

interface ComposeSpecShape {
  services?: {
    name?: string;
    cron?: {
      expression?: string;
      timezone?: string;
      timeout?: string;
    } | null;
  }[];
}

/**
 * 从归一化快照（canonical JSON 文本）抽取 cron 服务清单。解析失败返回
 * null（快照损坏/形态漂移——调用方呈现诚实提示，不伪造空清单）。
 */
export function extractCronServices(
  composeJSON: string | undefined,
): CronServiceView[] | null {
  if (!composeJSON) return [];
  let spec: ComposeSpecShape;
  try {
    spec = JSON.parse(composeJSON) as ComposeSpecShape;
  } catch {
    return null;
  }
  const services = Array.isArray(spec.services) ? spec.services : [];
  const out: CronServiceView[] = [];
  for (const s of services) {
    if (!s || typeof s.name !== "string" || !s.cron) continue;
    out.push({
      name: s.name,
      expression: s.cron.expression ?? "",
      timezone: s.cron.timezone,
      timeout: s.cron.timeout,
    });
  }
  return out;
}

/**
 * 服务清单抽取（同名快照的全体服务）：App 详情「Services」卡用——cron
 * 服务必须标注 scheduled 而非长驻态（不冒充 running）。
 */
export function extractServiceNames(
  composeJSON: string | undefined,
): { name: string; isCron: boolean }[] | null {
  if (!composeJSON) return [];
  let spec: ComposeSpecShape;
  try {
    spec = JSON.parse(composeJSON) as ComposeSpecShape;
  } catch {
    return null;
  }
  const services = Array.isArray(spec.services) ? spec.services : [];
  const out: { name: string; isCron: boolean }[] = [];
  for (const s of services) {
    if (!s || typeof s.name !== "string") continue;
    out.push({ name: s.name, isCron: Boolean(s.cron) });
  }
  return out;
}
