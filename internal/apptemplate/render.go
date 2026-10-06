// Render 是模板渲染单源（ADR-0050 决策 2）：values 校验（fail-closed）→
// 语义变量三型各得其所 → 插值后产物恰是 ComposeDoc。secret 值永不进渲染
// 产物——secret 变量的两个消费位都是"引用"：服务 secrets 列表重写为真名
// （template:<app>:<var>）、文本插值为文件路径（/run/secrets/<真名>）。
package apptemplate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/spec"
)

// Rendered 是一次渲染的产物。
type Rendered struct {
	// Compose 是插值后的 compose 受控子集（平台键已剥、secret 列表已重写），
	// 形态恰是 spec.ComposeDoc——直通 NormalizeCompose 喉。
	Compose spec.ComposeDoc
	// SecretNames 是 secret 变量名 → 铸造 Secret 实体名（template:<app>:<var>）。
	// 实例化方按此铸造/复用 Secret 并获得 create-or-reuse 报告锚。
	SecretNames map[string]string
	// Routes 是解析后的 Route 声明（host = domain 变量有效值）。
	Routes []ResolvedRoute
}

// ResolvedRoute 是一条已解析 host 的 Route 声明。
type ResolvedRoute struct {
	Host     string
	Process  string
	Port     int32
	Protocol string
}

// Render 渲染模板：appName 参与 secret 命名（template:<app>:<var>——ADR-0029
// database:<name> 命名先例）。values 只承载 string/domain 型用户值；secret 型
// 变量携带值即拒（平台铸造，用户面无覆写通道）。
func (d *Doc) Render(values map[string]string, appName string) (*Rendered, error) {
	if appName == "" {
		return nil, verr(templateKey, "appName must not be empty (the app name participates in generated secret names)")
	}
	declared := map[string]Variable{}
	for _, v := range d.Variables {
		declared[v.Name] = v
	}
	// values 校验：未知键拒、secret 携值拒；有效值集 = 用户值覆盖缺省。
	effective := map[string]string{}
	for _, v := range d.Variables {
		if v.Type != VarSecret {
			effective[v.Name] = v.Default
		}
	}
	for name, val := range values {
		v, ok := declared[name]
		if !ok {
			return nil, verr("values."+name, "unknown variable %q (declared: %s)", name, d.variableNames())
		}
		if v.Type == VarSecret {
			return nil, verr("values."+name,
				"secret variable %q is platform-generated; remove it from values (the value lands in the %s secret)", name, SecretName(appName, name))
		}
		if val == "" {
			return nil, verr("values."+name, "variable %q must not be empty (omit it to use the default)", name)
		}
		effective[name] = val
	}
	for _, v := range d.Variables {
		if v.Required && effective[v.Name] == "" {
			return nil, verr("values."+v.Name, "variable %q is required (no default declared)", v.Name)
		}
	}

	// secret 变量的插值形态 = 文件路径（值永不出场的机制面）。
	secretNames := map[string]string{}
	for _, v := range d.Variables {
		if v.Type != VarSecret {
			continue
		}
		name := SecretName(appName, v.Name)
		if !spec.ValidSecretName(name) {
			return nil, verr(templateKey+".variables",
				"secret variable %q resolves to secret name %q which fails the name pattern %q (shorten the app name)", v.Name, name, spec.SecretNamePattern)
		}
		secretNames[v.Name] = name
	}
	interpolated := make(map[string]string, len(effective)+len(secretNames))
	for name, val := range effective {
		interpolated[name] = val
	}
	for name, real := range secretNames {
		interpolated[name] = SecretFilePath(real)
	}

	out := &Rendered{SecretNames: secretNames}
	composeAny, err := renderCompose(d.Compose, interpolated, secretNames)
	if err != nil {
		return nil, err
	}
	compose, ok := composeAny.(map[string]any)
	if !ok {
		return nil, verr(templateKey, "rendered compose is not a mapping (internal error)")
	}
	out.Compose = compose

	// routes 解析：var 必须是 domain 型且已供值；process/port 必须在 compose
	// 服务声明面在场（Route 后端解析的期望集供给面）。
	svcPorts, err := declaredPorts(compose)
	if err != nil {
		return nil, err
	}
	for _, r := range d.Routes {
		v, ok := declared[r.Var]
		if !ok {
			return nil, verr(templateKey+".routes", "route var %q is not a declared variable", r.Var)
		}
		if v.Type != VarDomain {
			return nil, verr(templateKey+".routes", "route var %q must be a domain variable (got %s)", r.Var, v.Type)
		}
		host := effective[r.Var]
		if host == "" {
			return nil, verr("values."+r.Var, "domain variable %q has no value (route target host)", r.Var)
		}
		ports, ok := svcPorts[r.Process]
		if !ok {
			return nil, verr(templateKey+".routes", "route process %q is not a compose service (services: %v)", r.Process, sortedKeys(svcPorts))
		}
		if !ports[r.Port] {
			list := make([]string, 0, len(ports))
			for p := range ports {
				list = append(list, fmt.Sprintf("%d", p))
			}
			sort.Strings(list)
			return nil, verr(templateKey+".routes", "route port %d is not declared by service %q (declared: %v)", r.Port, r.Process, list)
		}
		out.Routes = append(out.Routes, ResolvedRoute{Host: host, Process: r.Process, Port: r.Port, Protocol: r.Protocol})
	}
	return out, nil
}

// SecretName 铸 secret 变量的实体名（单源公式；实例化与渲染共用）。
func SecretName(appName, varName string) string {
	return "template:" + appName + ":" + varName
}

// SecretFilePath 是 secret 实体名的文件注入路径（swarm translate 单源同款：
// /run/secrets/<名>——名即路径，无重映射）。
func SecretFilePath(secretName string) string {
	return "/run/secrets/" + secretName
}

// variableNames 是声明变量名列表（错误文案用，声明序）。
func (d *Doc) variableNames() string {
	if len(d.Variables) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(d.Variables))
	for _, v := range d.Variables {
		names = append(names, v.Name)
	}
	return strings.Join(names, ", ")
}

// renderCompose 深拷贝并插值：map 的值与 list 的元素递归；字符串值内的
// ${name} 孔按 interpolated 解析（未声明变量即拒——fail-closed，不留
// 残余孔）；secrets 列表（服务与 first boot job 共用键）重写 secret 变量
// 名为真名。键永不插值（结构面）。
func renderCompose(node any, interpolated, secretNames map[string]string) (any, error) {
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, val := range v {
			if key == "secrets" {
				rewritten, err := rewriteSecrets(val, secretNames)
				if err != nil {
					return nil, err
				}
				out[key] = rewritten
				continue
			}
			rendered, err := renderCompose(val, interpolated, secretNames)
			if err != nil {
				return nil, err
			}
			out[key] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			rendered, err := renderCompose(item, interpolated, secretNames)
			if err != nil {
				return nil, err
			}
			out = append(out, rendered)
		}
		return out, nil
	case string:
		return interpolate(v, interpolated)
	default:
		return node, nil
	}
}

// rewriteSecrets 重写 secrets 列表：条目 == secret 变量名 → 真名；其余条目
// 原样（字面 secret 引用——如 database:<name> 凭证真源）。插值孔在条目内
// 即拒（与重写语义分立：列表认变量名，不认插值语法）。
func rewriteSecrets(raw any, secretNames map[string]string) (any, error) {
	list, ok := raw.([]any)
	if !ok {
		return raw, nil // 形态错误留给 NormalizeCompose 的 composeSecretRefs 精确报
	}
	out := make([]any, 0, len(list))
	for i, entry := range list {
		s, isStr := entry.(string)
		if isStr && strings.Contains(s, "${") {
			return nil, verr("secrets", "secret entry %d (%q): interpolation is not supported here; list the secret variable name directly", i, s)
		}
		if isStr {
			if real, isVar := secretNames[s]; isVar {
				out = append(out, real)
				continue
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// interpolate 解析字符串内的全部 ${name} 孔（未声明变量即拒——错误带孔
// 原文与候选集）。
func interpolate(s string, interpolated map[string]string) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}
	var missing string
	out := interpolateRe.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-1]
		if val, ok := interpolated[name]; ok {
			return val
		}
		if missing == "" {
			missing = name
		}
		return match
	})
	if missing != "" {
		return "", verr("compose", "value %q references undeclared variable %q", s, missing)
	}
	return out, nil
}

// declaredPorts 从 compose 树提取各服务的声明端口集（短语法 "N[/proto]"，
// 与 spec.composePort 同认形态——Route 校验的期望集供给面）。
func declaredPorts(compose spec.ComposeDoc) (map[string]map[int32]bool, error) {
	out := map[string]map[int32]bool{}
	services, ok := compose["services"].(map[string]any)
	if !ok {
		return nil, verr("compose.services", "at least one service is required")
	}
	for name, raw := range services {
		ports := map[int32]bool{}
		svc, ok := raw.(map[string]any)
		if ok {
			if list, ok := svc["ports"].([]any); ok {
				for i, entry := range list {
					s, isStr := entry.(string)
					if !isStr {
						return nil, verr(fmt.Sprintf("compose.services.%s.ports[%d]", name, i),
							"port entry must be a string like \"8080\" or \"8080/h2c\"")
					}
					numPart, _, _ := strings.Cut(s, "/")
					var port int
					if _, err := fmt.Sscanf(numPart, "%d", &port); err != nil || port < 1 || port > 65535 {
						return nil, verr(fmt.Sprintf("compose.services.%s.ports[%d]", name, i), "port %q out of range", numPart)
					}
					ports[int32(port)] = true //nolint:gosec // 域内已检
				}
			}
		}
		out[name] = ports
	}
	return out, nil
}

// sortedKeys 是错误文案用的键排序列表。
func sortedKeys(m map[string]map[int32]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
