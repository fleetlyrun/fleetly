// dokploy 导出解析（F3.3，ADR-0050 决策 5：竞品迁移钩子的纯叶子半边）：
// dokploy 无官方全量导出格式——输入契约钉其 API 数据模型字段
// （docs.dokploy.com/docs/api：applications/compose/domains/databases 分立
// 资源），接受手组或截取自其 API 的 JSON。产物是迁移计划（部署意图 +
// 逐条 skip 报告），不在此搬移数据（Volume/数据库内容仍走 Backup/Restore
// 面）。映射面 fail-closed：compose 插值孔未消解即整条 skip，不静默落
// 字面 "${...}" 值。
package spec

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// DokployExport 是输入面（字段名随 dokploy API 数据模型——camelCase 原样）。
type DokployExport struct {
	Applications []DokployApplication `json:"applications"`
	Compose      []DokployCompose     `json:"compose"`
	Domains      []DokployDomain      `json:"domains"`
	Databases    []DokployDatabase    `json:"databases"`
}

// DokployApplication 是 dokploy 的 application 资源（buildType 取值
// dockerfile|dockerimage|static；git 源形态 sourceType 非空）。
type DokployApplication struct {
	ApplicationID string `json:"applicationId"`
	Name          string `json:"name"`
	BuildType     string `json:"buildType"`
	DockerImage   string `json:"dockerImage"`
	Env           string `json:"env"`
	SourceType    string `json:"sourceType"`
	Repository    string `json:"repository"`
}

// DokployCompose 是 dokploy 的 compose 资源（composeContent 是部署体）。
type DokployCompose struct {
	ComposeID      string `json:"composeId"`
	Name           string `json:"name"`
	ComposeContent string `json:"composeContent"`
	Env            string `json:"env"`
}

// DokployDomain 是 dokploy 的 domain 资源（挂 application 或 compose）。
type DokployDomain struct {
	DomainID      string `json:"domainId"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	ApplicationID string `json:"applicationId"`
	ComposeID     string `json:"composeId"`
	ServiceName   string `json:"serviceName"`
}

// DokployDatabase 是 dokploy 的 database 资源（type 是其引擎词）。
type DokployDatabase struct {
	DatabaseID string `json:"databaseId"`
	Name       string `json:"name"`
	Type       string `json:"type"`
}

// DokployPlan 是迁移计划：逐 App 部署意图 + 逐库引擎映射 + 逐条 skip。
type DokployPlan struct {
	Apps      []DokployAppPlan
	Databases []DokployDatabasePlan
	Skipped   []DokploySkip
}

// DokployAppPlan 是一个 App 的部署意图（image 直投与 compose 二选一）。
type DokployAppPlan struct {
	Name        string
	Image       string
	ComposeYAML string
	Env         map[string]string
	Routes      []DokployRoutePlan
}

// DokployRoutePlan 是一条 Route 意图。
type DokployRoutePlan struct {
	Host    string
	Process string
	Port    int32
}

// DokployDatabasePlan 是一个 Database 意图（engine 已映射为 fleetly 词）。
type DokployDatabasePlan struct {
	Name   string
	Engine string
}

// DokploySkip 是一条不可映射记录（带原因，不静默丢）。
type DokploySkip struct {
	Item   string
	Kind   string
	Reason string
}

// dokployEngineMap 是 dokploy 引擎词 → fleetly 引擎词（值域外的显式 skip；
// 引擎在册性由 CreateDatabase 受理位单源执法——本表只做词面映射）。
var dokployEngineMap = map[string]string{
	"postgres": "postgres",
	"mysql":    "mysql",
	"mongo":    "mongo",
	"redis":    "redis",
}

// ParseDokploy 解析导出文档为迁移计划（叶子纯函数；engine 在册性、
// compose 白名单、名字合法性的最终执法在服务端受理位——此处只产出意图
// 与诚实 skip）。
func ParseDokploy(body []byte) (*DokployPlan, error) {
	var ex DokployExport
	if err := json.Unmarshal(body, &ex); err != nil {
		return nil, fmt.Errorf("dokploy: export is not valid JSON: %w", err)
	}
	plan := &DokployPlan{}
	appByID := map[string]int{} // dokploy applicationId → plan.Apps 下标
	for i := range ex.Applications {
		a := ex.Applications[i]
		if strings.TrimSpace(a.Name) == "" {
			plan.Skipped = append(plan.Skipped, DokploySkip{
				Kind: "application", Item: a.ApplicationID,
				Reason: "carries no name; dokploy exports of draft rows cannot be mapped",
			})
			continue
		}
		if a.BuildType != "dockerimage" || strings.TrimSpace(a.DockerImage) == "" {
			plan.Skipped = append(plan.Skipped, DokploySkip{
				Kind: "application", Item: a.Name,
				Reason: fmt.Sprintf(
					"build type %q with repository %q is a build-source app; re-deploy it from fleetly source intake (uploads or git push) and migrate only its configuration here",
					a.BuildType, a.Repository),
			})
			continue
		}
		appByID[a.ApplicationID] = len(plan.Apps)
		plan.Apps = append(plan.Apps, DokployAppPlan{
			Name:  a.Name,
			Image: strings.TrimSpace(a.DockerImage),
			Env:   parseDokployEnv(a.Env),
		})
	}
	composeByID := map[string]int{}
	for i := range ex.Compose {
		c := ex.Compose[i]
		if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.ComposeContent) == "" {
			plan.Skipped = append(plan.Skipped, DokploySkip{
				Kind: "compose", Item: c.Name,
				Reason: "carries no name or no composeContent; dokploy exports of draft rows cannot be mapped",
			})
			continue
		}
		env := parseDokployEnv(c.Env)
		content := c.ComposeContent
		if unresolved := substituteDokployVars(&content, env); len(unresolved) > 0 {
			sort.Strings(unresolved)
			plan.Skipped = append(plan.Skipped, DokploySkip{
				Kind: "compose", Item: c.Name,
				Reason: fmt.Sprintf(
					"compose references variables absent from the dokploy env panel (%s); fleetly compose intake lands unresolved holes as literal values, so this app is skipped rather than silently corrupted",
					strings.Join(unresolved, ", ")),
			})
			continue
		}
		composeByID[c.ComposeID] = len(plan.Apps)
		plan.Apps = append(plan.Apps, DokployAppPlan{
			Name: c.Name, ComposeYAML: content, Env: env,
		})
	}
	for i := range ex.Domains {
		d := ex.Domains[i]
		idx := -1
		if d.ApplicationID != "" {
			if j, ok := appByID[d.ApplicationID]; ok {
				idx = j
			}
		} else if d.ComposeID != "" {
			if j, ok := composeByID[d.ComposeID]; ok {
				idx = j
			}
		}
		if idx < 0 {
			anchor := d.ApplicationID
			if anchor == "" {
				anchor = d.ComposeID
			}
			plan.Skipped = append(plan.Skipped, DokploySkip{
				Kind: "domain", Item: d.Host,
				Reason: fmt.Sprintf("attaches to resource %s which is itself unmapped (see its skip entry)", firstNonEmpty(anchor, d.DomainID)),
			})
			continue
		}
		process := d.ServiceName
		if process == "" {
			process = "web"
		}
		plan.Apps[idx].Routes = append(plan.Apps[idx].Routes, DokployRoutePlan{
			Host: d.Host, Process: process, Port: int32(d.Port), //nolint:gosec // 端口域执法在受理位
		})
	}
	for i := range ex.Databases {
		db := ex.Databases[i]
		engine, ok := dokployEngineMap[strings.ToLower(strings.TrimSpace(db.Type))]
		if !ok {
			plan.Skipped = append(plan.Skipped, DokploySkip{
				Kind: "database", Item: db.Name,
				Reason: fmt.Sprintf("dokploy engine %q has no fleetly mapping (engines: postgres, mysql, mongo, redis)", db.Type),
			})
			continue
		}
		plan.Databases = append(plan.Databases, DokployDatabasePlan{Name: db.Name, Engine: engine})
	}
	return plan, nil
}

// parseDokployEnv 解析 dokploy 的 raw env 文本（KEY=VALUE 行；注释/空行跳过）。
func parseDokployEnv(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(k) == "" {
			continue
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// substituteDokployVars 把 env 面替换进 compose 文本的 ${KEY} 孔（dokploy
// 的 env 面语义 = compose 插值）。返回未消解孔的键集（fail-closed 消费）。
func substituteDokployVars(content *string, env map[string]string) []string {
	if !strings.Contains(*content, "${") {
		return nil
	}
	var unresolved []string
	seen := map[string]bool{}
	replaced := dokployVarRe.ReplaceAllStringFunc(*content, func(match string) string {
		key := match[2 : len(match)-1]
		if val, ok := env[key]; ok {
			return val
		}
		if !seen[key] {
			seen[key] = true
			unresolved = append(unresolved, key)
		}
		return match
	})
	*content = replaced
	return unresolved
}

// dokployVarRe 是 compose 插值孔形态（${KEY}——与 apptemplate 的渲染孔
// 同形；本包与 apptemplate 分立，正则各持一份叶子拷贝）。
var dokployVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
