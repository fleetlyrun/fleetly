package cmd

// templates 命令组（F3.3，ADR-0050）：目录读面（list/show）+ 一键部署
// （instantiate——服务端 create-or-reuse 编排，CLI 侧唯一增值 = 缺省等待
// 与 sslip 缺省 host 拼装）+ 目录刷新（refresh——操作员动词，未配置
// catalog URL 时服务端精确拒绝）。

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/lynx-go/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// templateReport 是 instantiate 的 --json 形态。
type templateReport struct {
	Template   string             `json:"template"`
	Version    string             `json:"version"`
	AppID      string             `json:"app_id"`
	Deployment string             `json:"deployment_id"`
	State      string             `json:"state"`
	Resources  []templateResource `json:"resources"`
}

type templateResource struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reused bool   `json:"reused"`
}

func newTemplatesListVerb() commands.Command {
	const name = "list"
	return &flaggedVerb{
		name:     name,
		synopsis: "List the app template catalog (embedded by default; the last refreshed snapshot when one exists)",
		usage:    "templates list",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			list, err := c.Templates.ListTemplates(ctx, &deliveryv1.ListTemplatesRequest{})
			if err != nil {
				return err
			}
			if jsonOut {
				type entry struct {
					Name        string `json:"name"`
					Version     string `json:"version"`
					Digest      string `json:"digest"`
					Description string `json:"description"`
				}
				out := struct {
					Source    string  `json:"source"`
					Templates []entry `json:"templates"`
				}{Source: list.GetSource()}
				for _, t := range list.GetTemplates() {
					out.Templates = append(out.Templates, entry{
						Name: t.GetName(), Version: t.GetVersion(),
						Digest: t.GetDigest(), Description: t.GetDescription(),
					})
				}
				return writeJSON(env.Stdout, out)
			}
			_, err = fmt.Fprintf(env.Stdout, "source %s\n", list.GetSource())
			if err != nil {
				return err
			}
			for _, t := range list.GetTemplates() {
				_, err = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\n", t.GetName(), t.GetVersion(), t.GetDescription())
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newTemplatesShowVerb() commands.Command {
	const name = "show"
	return &flaggedVerb{
		name:     name,
		synopsis: "Show one template: variable declarations and the compose body",
		usage:    "templates show NAME",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := exactArgs(name, 1, args); err != nil {
				return err
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			got, err := c.Templates.GetTemplate(ctx, &deliveryv1.GetTemplateRequest{Name: args[0]})
			if err != nil {
				return err
			}
			t := got.GetTemplate()
			if jsonOut {
				type variable struct {
					Name        string `json:"name"`
					Type        string `json:"type"`
					Description string `json:"description"`
					Default     string `json:"default,omitempty"`
					Required    bool   `json:"required,omitempty"`
				}
				out := struct {
					Name        string     `json:"name"`
					Version     string     `json:"version"`
					Digest      string     `json:"digest"`
					Description string     `json:"description"`
					Variables   []variable `json:"variables,omitempty"`
					Body        string     `json:"body"`
				}{Name: t.GetName(), Version: t.GetVersion(), Digest: t.GetDigest(), Description: t.GetDescription(), Body: got.GetBody()}
				for _, v := range t.GetVariables() {
					out.Variables = append(out.Variables, variable{
						Name: v.GetName(), Type: v.GetType(), Description: v.GetDescription(),
						Default: v.GetDefault(), Required: v.GetRequired(),
					})
				}
				return writeJSON(env.Stdout, out)
			}
			_, err = fmt.Fprintf(env.Stdout, "%s %s (%s)\n%s\n", t.GetName(), t.GetVersion(), t.GetDigest(), t.GetDescription())
			if err != nil {
				return err
			}
			if len(t.GetVariables()) > 0 {
				_, _ = fmt.Fprintln(env.Stdout, "variables:")
				for _, v := range t.GetVariables() {
					required := ""
					if v.GetRequired() {
						required = " (required)"
					}
					_, err = fmt.Fprintf(env.Stdout, "  %s: %s%s — %s\n", v.GetName(), v.GetType(), required, v.GetDescription())
					if err != nil {
						return err
					}
				}
			}
			_, err = fmt.Fprintln(env.Stdout, strings.TrimRight(got.GetBody(), "\n"))
			return err
		},
	}
}

// sslipDefaultHost 是 quickstart 的 sslip 拼装复用（一键部署的 CLI 缺省：
// 必填 domain 变量未供值且恰一枚时拼 <app>.<ip>.sslip.io；多枚域名变量
// 要求显式 --set——同缺省会互撞）。
func sslipDefaultHost(addr, appName string) string { return sslipHost(addr, appName) }

// ensureProjectByName 按名 create-or-reuse 项目（quickstart 同款幂等链）。
func ensureProjectByName(ctx context.Context, c *sdk.Client, name string) (string, error) {
	plist, err := c.Projects.ListProjects(ctx, &structurev1.ListProjectsRequest{})
	if err != nil {
		return "", err
	}
	for _, p := range plist.GetProjects() {
		if p.GetName() == name {
			return p.GetId(), nil
		}
	}
	created, err := c.Projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: name})
	if err != nil {
		return "", err
	}
	return created.GetProject().GetId(), nil
}

func newTemplatesInstantiateVerb() commands.Command {
	const name = "instantiate"
	var project, appName string
	var noWait bool
	var sets envSlice
	return &flaggedVerb{
		name:     name,
		synopsis: "Deploy an app from a catalog template (server-side create-or-reuse: app, secrets, databases, route, then the existing deploy chain)",
		usage:    "templates instantiate TEMPLATE --project NAME --app NAME [--set KEY=VALUE]... [--no-wait]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project name (reused if it exists, created otherwise)")
			fs.StringVar(&appName, "app", "", "app name inside the project (reused if it exists — re-running re-deploys)")
			fs.Var(&sets, "set", "template variable KEY=VALUE, repeatable (string and domain types; secret variables are platform-generated)")
			fs.BoolVar(&noWait, "no-wait", false, "submit and print without waiting for the deployment")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := exactArgs(name, 1, args); err != nil {
				return err
			}
			if project == "" || appName == "" {
				return usageErr(name, "--project and --app are required")
			}
			var dialOpts []dialOption
			if !noWait {
				dialOpts = append(dialOpts, noDeadline()) // 等待是流式长等待
			}
			ctx, cancel, c, err := dialFromEnv(ctx, dialOpts...)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			serverAddr := envOr(envAddr, defaultAddr)

			// project create-or-reuse（按名，quickstart 同款）。
			projectID, err := ensureProjectByName(ctx, c, project)
			if err != nil {
				return err
			}

			// 模板读面：值缺省（唯一必填 domain 变量 → sslip 缺省 host）。
			got, err := c.Templates.GetTemplate(ctx, &deliveryv1.GetTemplateRequest{Name: args[0]})
			if err != nil {
				return err
			}
			values, err := parseTemplateSets(sets, got.GetTemplate(), serverAddr, appName)
			if err != nil {
				return usageErr(name, err.Error())
			}

			res, err := c.Templates.InstantiateTemplate(ctx, &deliveryv1.InstantiateTemplateRequest{
				ProjectId: projectID, Template: args[0], AppName: appName, Values: values,
			})
			if err != nil {
				return err
			}
			state := "queued"
			if !noWait {
				if state, err = waitDeploymentFrames(ctx, c, res.GetDeploymentId(), nil); err != nil {
					return err
				}
			}
			report := templateReport{
				Template: args[0], Version: res.GetTemplateVersion(),
				AppID: res.GetAppId(), Deployment: res.GetDeploymentId(), State: state,
			}
			for _, r := range res.GetResources() {
				report.Resources = append(report.Resources, templateResource{
					Kind: r.GetKind(), ID: r.GetId(), Name: r.GetName(), Reused: r.GetReused(),
				})
			}
			if jsonOut {
				return writeJSON(env.Stdout, report)
			}
			for _, r := range report.Resources {
				tag := "created"
				if r.Reused {
					tag = "reused"
				}
				label := r.Name
				if label == "" {
					label = r.ID
				}
				_, err = fmt.Fprintf(env.Stdout, "%s %s %s\n", r.Kind, label, tag)
				if err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(env.Stdout, "deployment %s %s\n", report.Deployment, state)
			if err != nil {
				return err
			}
			// 有 domain 值时给出可打开的 URL 面（一键部署的收口感）。
			var hosts []string
			for _, v := range got.GetTemplate().GetVariables() {
				if v.GetType() == "domain" {
					if h := values[v.GetName()]; h != "" {
						hosts = append(hosts, h)
					}
				}
			}
			sort.Strings(hosts)
			for _, h := range hosts {
				_, err = fmt.Fprintf(env.Stdout, "open http://%s\n", h)
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// parseTemplateSets 组装 values：--set 的 K=V + 缺省规则（唯一必填 domain
// 变量未供值 → sslip 形态；secret 型携带即拒在服务端，CLI 先拒省往返）。
func parseTemplateSets(sets envSlice, t *deliveryv1.Template, serverAddr, appName string) (map[string]string, error) {
	values := map[string]string{}
	for _, kv := range sets {
		k, v, found := strings.Cut(kv, "=")
		if !found || k == "" {
			return nil, fmt.Errorf("--set %q must be KEY=VALUE", kv)
		}
		values[k] = v
	}
	var requiredDomains []string
	for _, v := range t.GetVariables() {
		switch v.GetType() {
		case "secret":
			if _, ok := values[v.GetName()]; ok {
				return nil, fmt.Errorf("secret variable %q is platform-generated; remove it from --set", v.GetName())
			}
		case "domain":
			if v.GetRequired() {
				if _, ok := values[v.GetName()]; !ok {
					requiredDomains = append(requiredDomains, v.GetName())
				}
			}
		}
	}
	if len(requiredDomains) == 1 {
		values[requiredDomains[0]] = sslipDefaultHost(serverAddr, appName)
	}
	return values, nil
}

func newTemplatesRefreshVerb() commands.Command {
	const name = "refresh"
	return &flaggedVerb{
		name:     name,
		synopsis: "Refresh the template catalog from the configured catalog URL (operator action; fails closed — a bad catalog never replaces a working one)",
		usage:    "templates refresh",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			res, err := c.Templates.RefreshTemplates(ctx, &deliveryv1.RefreshTemplatesRequest{})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(env.Stdout, struct {
					PreviousDigest string `json:"previous_digest"`
					Digest         string `json:"digest"`
					TemplateCount  int32  `json:"template_count"`
				}{res.GetPreviousDigest(), res.GetDigest(), res.GetTemplateCount()})
			}
			_, err = fmt.Fprintf(env.Stdout, "catalog refreshed %s -> %s (%d templates)\n",
				res.GetPreviousDigest(), res.GetDigest(), res.GetTemplateCount())
			return err
		},
	}
}
