// 模板实例化表单的纯函数面（F3.3，ADR-0050）：变量声明 → 表单字段形态、
// 表单状态 → values 载荷。secret 型不进表单（平台铸造——服务端与 CLI 同
// 一执法）；空值不发送（undefined 语义 = 用模板缺省或触发 required 拒绝，
// 与 deployPayload 的"空值不发送"同口径）。

export interface TemplateVariableDecl {
  name: string;
  type: string;
  description: string;
  default: string;
  required: boolean;
}

/** inputKindOf 变量声明的表单形态：secret 不进表单，domain/string 文本输入。 */
export function inputKindOf(variable: TemplateVariableDecl): "text" | "none" {
  return variable.type === "secret" ? "none" : "text";
}

/** buildTemplateValues 表单状态 → InstantiateTemplate values（空串剔除、secret 型恒缺席）。 */
export function buildTemplateValues(
  variables: TemplateVariableDecl[],
  form: Record<string, string>,
): Record<string, string> {
  const values: Record<string, string> = {};
  for (const v of variables) {
    if (v.type === "secret") continue;
    const value = (form[v.name] ?? "").trim();
    if (value !== "") values[v.name] = value;
  }
  return values;
}
