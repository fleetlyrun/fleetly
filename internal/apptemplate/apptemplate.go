// Package apptemplate 是 App 模板库的叶子实现（ADR-0050）：模板文档 =
// compose 受控子集 + 两个平台扩展键（x-fleetly-template / x-fleetly-databases）。
// 解析 fail-closed（扩展键内未知字段即拒）、渲染单源（语义变量三型：string
// 插值 / secret 铸造引用 / domain 驱动 Route）——secret 值永不进渲染产物
// （ADR-0014 红线），渲染产物经 spec.NormalizeCompose 白名单喉（与手写
// compose 同一条执法）。内嵌目录与远端清单校验也在本包（digest 核对 +
// 全量预校验，ADR-0045 冷启动诚实边界同款）。
//
// 与 internal/engine/dbtemplate（Database 模板 = 引擎钉版知识）分立：本包
// 产出部署输入，不携带任何引擎方言。
package apptemplate

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/fleetlyrun/fleetly/internal/engine/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/spec"
)

// VariableType 是语义变量三型（ADR-0050 决策 2）。
type VariableType string

const (
	// VarString 文本值：${name} 插值进 compose 文本。
	VarString VariableType = "string"
	// VarSecret 自动生成密码：平台铸造随机值落 Secret template:<app>:<var>；
	// 服务 secrets 列表按名重写为真名，文本插值为文件路径（值永不出现）。
	VarSecret VariableType = "secret"
	// VarDomain 域名：值 = host，与 x-fleetly-template.routes 联动创建 Route。
	VarDomain VariableType = "domain"
)

// templateKey 与 databasesKey 是模板文档的两个平台扩展键（x-fleetly-first-boot-jobs
// 是既有 compose 扩展键，不经本包翻译）。
const (
	templateKey  = "x-fleetly-template"
	databasesKey = "x-fleetly-databases"
)

// Variable 是一条变量声明。
type Variable struct {
	Name        string
	Type        VariableType
	Description string
	Default     string
	Required    bool
}

// RouteDecl 是 x-fleetly-template.routes 的一条声明：domain 变量驱动一条
// Route（process/port 必须在 compose 服务声明面在场——Render 校验）。
type RouteDecl struct {
	Var      string
	Process  string
	Port     int32
	Protocol string // 缺省 http（Route 同词汇 http|h2c|tcp）
}

// DatabaseDecl 是 x-fleetly-databases 的一条声明：实例化时 create-or-reuse
// 同名托管 Database（凭证单真源链复用，ADR-0029 决策 6 原样）。
type DatabaseDecl struct {
	Name   string
	Engine string
}

// Doc 是解析后的模板文档：两个平台扩展键已剥离，Compose 恰是
// spec.ComposeDoc 形态（services/volumes/x-fleetly-first-boot-jobs）。
type Doc struct {
	Name        string
	Version     string
	Description string
	Variables   []Variable
	Routes      []RouteDecl
	Databases   []DatabaseDecl
	Compose     spec.ComposeDoc
}

// TemplateNamePattern 钉死模板名与数据库名形态（目录键，kebab-case）。
const TemplateNamePattern = `[a-z][a-z0-9-]{0,62}`

var (
	templateNameRe = regexp.MustCompile(`^` + TemplateNamePattern + `$`)
	varNameRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	// interpolateRe 是 ${name} 插值孔的形态（compose 文本值内）。
	interpolateRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// Parse 解析并校验一份模板文档（fail-closed：平台扩展键内未知字段、未知
// 变量类型、重名、非法名即拒；compose 侧白名单由 NormalizeCompose 喉承载，
// 此处只预检顶层键域）。坏文档零可用产物——目录刷新预校验与实例化共用
// 本入口。
func Parse(body []byte) (*Doc, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, verr(templateKey, "invalid YAML: %v", err)
	}
	if doc == nil {
		return nil, verr(templateKey, "document is empty")
	}
	for key := range doc {
		switch key {
		case "services", "volumes", "x-fleetly-first-boot-jobs", templateKey, databasesKey:
		default:
			return nil, verr("template."+key,
				"unsupported top-level field %q (supported: services, volumes, x-fleetly-first-boot-jobs, %s, %s)", key, templateKey, databasesKey)
		}
	}
	meta, ok := doc[templateKey].(map[string]any)
	if !ok {
		return nil, verr(templateKey, "every template document must carry a %s block", templateKey)
	}
	for key := range meta {
		switch key {
		case "name", "version", "description", "variables", "routes":
		default:
			return nil, verr(templateKey+"."+key, "unsupported field %q (supported: name, version, description, variables, routes)", key)
		}
	}
	out := &Doc{}
	name, _ := meta["name"].(string)
	if !templateNameRe.MatchString(name) {
		return nil, verr(templateKey+".name", "template name %q must match %q", name, TemplateNamePattern)
	}
	out.Name = name
	version, _ := meta["version"].(string)
	if strings.TrimSpace(version) == "" || strings.ContainsAny(version, " \t") {
		return nil, verr(templateKey+".version", "version must be a non-empty semver-like string without whitespace (got %q)", version)
	}
	out.Version = version
	out.Description, _ = meta["description"].(string)

	if rawVars, present := meta["variables"]; present && rawVars != nil {
		list, ok := rawVars.([]any)
		if !ok {
			return nil, verr(templateKey+".variables", "variables must be a list")
		}
		seen := map[string]bool{}
		for i, rawVar := range list {
			field := fmt.Sprintf("%s.variables[%d]", templateKey, i)
			m, ok := rawVar.(map[string]any)
			if !ok {
				return nil, verr(field, "variable must be a mapping")
			}
			for key := range m {
				switch key {
				case "name", "type", "description", "default", "required":
				default:
					return nil, verr(field+"."+key, "unsupported variable field %q (supported: name, type, description, default, required)", key)
				}
			}
			v := Variable{}
			v.Name, _ = m["name"].(string)
			if !varNameRe.MatchString(v.Name) {
				return nil, verr(field+".name", "variable name %q must match %q", v.Name, `[A-Za-z_][A-Za-z0-9_]{0,63}`)
			}
			if seen[v.Name] {
				return nil, verr(field+".name", "duplicate variable name %q", v.Name)
			}
			seen[v.Name] = true
			v.Type = VariableType(fmt.Sprintf("%v", m["type"]))
			switch v.Type {
			case VarString, VarSecret, VarDomain:
			default:
				return nil, verr(field+".type", "variable %s: type must be string, secret or domain (got %q)", v.Name, v.Type)
			}
			v.Description, _ = m["description"].(string)
			v.Default, _ = m["default"].(string)
			if req, present := m["required"]; present {
				b, ok := req.(bool)
				if !ok {
					return nil, verr(field+".required", "variable %s: required must be a boolean", v.Name)
				}
				v.Required = b
			}
			// secret 型恒平台铸造：default/required 无意义即拒（携带值同拒——
			// 用户面无覆写通道，dbtemplate 密码铸造同口径）。string/domain 的
			// required 语义 = 实例化必须供值（Render 执法），此处不重复。
			if v.Type == VarSecret && (v.Default != "" || v.Required) {
				return nil, verr(field, "secret variable %s is always platform-generated; default and required are not settable", v.Name)
			}
			out.Variables = append(out.Variables, v)
		}
	}

	if rawRoutes, present := meta["routes"]; present && rawRoutes != nil {
		list, ok := rawRoutes.([]any)
		if !ok {
			return nil, verr(templateKey+".routes", "routes must be a list")
		}
		for i, rawRoute := range list {
			field := fmt.Sprintf("%s.routes[%d]", templateKey, i)
			m, ok := rawRoute.(map[string]any)
			if !ok {
				return nil, verr(field, "route must be a mapping")
			}
			for key := range m {
				switch key {
				case "var", "process", "port", "protocol":
				default:
					return nil, verr(field+"."+key, "unsupported route field %q (supported: var, process, port, protocol)", key)
				}
			}
			r := RouteDecl{}
			r.Var, _ = m["var"].(string)
			r.Process, _ = m["process"].(string)
			port, _ := m["port"].(int)
			if r.Var == "" || r.Process == "" {
				return nil, verr(field, "route var and process must be non-empty")
			}
			if port < 1 || port > 65535 {
				return nil, verr(field+".port", "port %d out of range (1-65535)", port)
			}
			r.Port = int32(port) //nolint:gosec // 域内已检
			r.Protocol, _ = m["protocol"].(string)
			switch r.Protocol {
			case "", "http", "h2c", "tcp":
			default:
				return nil, verr(field+".protocol", "protocol %q must be http, h2c or tcp", r.Protocol)
			}
			out.Routes = append(out.Routes, r)
		}
	}

	if rawDBs, present := doc[databasesKey]; present && rawDBs != nil {
		list, ok := rawDBs.([]any)
		if !ok {
			return nil, verr(databasesKey, "must be a list of {name, engine} mappings")
		}
		seen := map[string]bool{}
		for i, rawDB := range list {
			field := fmt.Sprintf("%s[%d]", databasesKey, i)
			m, ok := rawDB.(map[string]any)
			if !ok {
				return nil, verr(field, "database must be a mapping")
			}
			for key := range m {
				switch key {
				case "name", "engine":
				default:
					return nil, verr(field+"."+key, "unsupported database field %q (supported: name, engine)", key)
				}
			}
			d := DatabaseDecl{}
			d.Name, _ = m["name"].(string)
			if !templateNameRe.MatchString(d.Name) {
				return nil, verr(field+".name", "database name %q must match %q", d.Name, TemplateNamePattern)
			}
			if seen[d.Name] {
				return nil, verr(field+".name", "duplicate database name %q", d.Name)
			}
			seen[d.Name] = true
			d.Engine, _ = m["engine"].(string)
			if !dbEngineRegistered(d.Engine) {
				return nil, verr(field+".engine", "engine %q is not a registered database template (available: %v)", d.Engine, dbtemplate.Engines())
			}
			out.Databases = append(out.Databases, d)
		}
	}

	// compose 子集剥离平台键后成为渲染基座；services 在场由 NormalizeCompose
	// 喉执法（此处不重复，坏文档零可用产物的预检面已在顶层键域）。
	compose := spec.ComposeDoc{}
	for key, val := range doc {
		if key == templateKey || key == databasesKey {
			continue
		}
		compose[key] = val
	}
	out.Compose = compose
	return out, nil
}

// dbEngineRegistered 校验引擎在册（dbtemplate 注册表单源——本包不复制值域）。
func dbEngineRegistered(engine string) bool {
	_, ok := dbtemplate.For(engine)
	return ok
}

// verr 构造 spec.ValidationError（api 层 mapValidationError 直收——模板面
// 错误与手写 compose 同一信封形态）。
func verr(field, format string, args ...any) error {
	return &spec.ValidationError{Field: field, Reason: fmt.Sprintf(format, args...)}
}
