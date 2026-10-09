package cmd

// create-from-dokploy（F3.3，ADR-0050 决策 5：竞品迁移钩子）：解析
// dokploy 导出 JSON → 迁移计划（逐 App 部署意图 + 引擎映射 + 逐条 skip
// 报告——不静默丢）→ --dry-run 只报计划；缺省执行态走既有 API（project
// create-or-reuse → databases create-or-reuse → app create-or-reuse →
// deploy（image/compose）→ routes create-or-reuse）。数据面不搬移
// （Volume/数据库内容走 Backup/Restore——skip 报告明示）。

import (
	"context"

	"flag"
	"fmt"
	"os"

	"github.com/lynx-go/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/spec"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// dokployReport 是 --json 形态（dry-run 与执行态共用骨架；执行态附结果列）。
type dokployReport struct {
	Plan  *spec.DokployPlan `json:"plan"`
	Steps []dokployStep     `json:"steps,omitempty"`
}

type dokployStep struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Reused bool   `json:"reused,omitempty"`
}

func newCreateFromDokployVerb() commands.Command {
	const name = "create-from-dokploy"
	var project, file string
	var dryRun bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Migrate a dokploy export into this project (parse to a plan, then create-or-reuse apps, databases and routes; data is not moved — it stays on the source host)",
		usage:    "create-from-dokploy --file export.json --project NAME [--dry-run]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&file, "file", "", "path to a dokploy export JSON (applications, compose, domains, databases arrays per the dokploy API model)")
			fs.StringVar(&project, "project", "", "target project name (reused if it exists, created otherwise)")
			fs.BoolVar(&dryRun, "dry-run", false, "print the migration plan (including every skipped item) without calling the platform")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if file == "" || project == "" {
				return usageErr(name, "--file and --project are required")
			}
			body, err := os.ReadFile(file) //nolint:gosec // 路径是用户显式 --file 旗标，CLI 本机工具形态
			if err != nil {
				return usageErr(name, fmt.Sprintf("--file: %v", err))
			}
			plan, err := spec.ParseDokploy(body)
			if err != nil {
				return usageErr(name, err.Error())
			}
			report := dokployReport{Plan: plan}
			printPlan := func() error {
				if jsonOut {
					return nil // json 形态整体一次写
				}
				for _, db := range plan.Databases {
					if _, err := fmt.Fprintf(env.Stdout, "database %s (engine %s)\n", db.Name, db.Engine); err != nil {
						return err
					}
				}
				for _, app := range plan.Apps {
					form := "image " + app.Image
					if app.ComposeYAML != "" {
						form = "compose"
					}
					if _, err := fmt.Fprintf(env.Stdout, "app %s (%s)\n", app.Name, form); err != nil {
						return err
					}
					for _, r := range app.Routes {
						if _, err := fmt.Fprintf(env.Stdout, "  route %s -> %s:%d\n", r.Host, r.Process, r.Port); err != nil {
							return err
						}
					}
				}
				for _, s := range plan.Skipped {
					if _, err := fmt.Fprintf(env.Stdout, "skipped %s %q: %s\n", s.Kind, s.Item, s.Reason); err != nil {
						return err
					}
				}
				return nil
			}
			if dryRun {
				if err := printPlan(); err != nil {
					return err
				}
				if jsonOut {
					return writeJSON(env.Stdout, report)
				}
				_, err := fmt.Fprintln(env.Stdout, "dry run: nothing was created")
				return err
			}

			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径

			projectID, err := ensureProjectByName(ctx, c, project)
			if err != nil {
				return err
			}
			report.Steps = append(report.Steps, dokployStep{Kind: "project", Name: project, Detail: projectID})

			// databases create-or-reuse（按名——重跑收敛）。
			for _, db := range plan.Databases {
				id, reused, err := ensureDatabaseByName(ctx, c, projectID, db.Name, db.Engine)
				if err != nil {
					return err
				}
				report.Steps = append(report.Steps, dokployStep{Kind: "database", Name: db.Name, Detail: id, Reused: reused})
			}

			// routes 全量拉取一次（host 复用判定——host 全局唯一）。
			existingRoutes, err := c.Routes.ListRoutes(ctx, &proxyv1.ListRoutesRequest{})
			if err != nil {
				return err
			}
			routeByHost := map[string]string{}
			for _, r := range existingRoutes.GetRoutes() {
				routeByHost[r.GetHost()] = r.GetId()
			}

			for _, app := range plan.Apps {
				appID, reused, err := ensureAppByName(ctx, c, projectID, app.Name)
				if err != nil {
					return err
				}
				report.Steps = append(report.Steps, dokployStep{Kind: "app", Name: app.Name, Detail: appID, Reused: reused})
				deploy := &deliveryDeployInput{AppID: appID, Env: app.Env}
				if app.ComposeYAML != "" {
					deploy.ComposeYAML = app.ComposeYAML
				} else {
					deploy.Image = app.Image
					// image 直投必须带端口声明（route 后端解析锚——staging
					// 真机咬出：无声明则 route 永远 backend unresolved，
					// 与 quickstart 直投同款教训）；取首条 route 的 port。
					if len(app.Routes) > 0 && app.Routes[0].Port > 0 {
						deploy.Port = app.Routes[0].Port
					}
				}
				depID, err := deployViaAPI(ctx, c, deploy)
				if err != nil {
					return err
				}
				report.Steps = append(report.Steps, dokployStep{Kind: "deployment", Name: app.Name, Detail: depID})
				for _, r := range app.Routes {
					if _, ok := routeByHost[r.Host]; ok {
						report.Steps = append(report.Steps, dokployStep{Kind: "route", Name: r.Host, Reused: true})
						continue
					}
					created, err := c.Routes.CreateRoute(ctx, &proxyv1.CreateRouteRequest{
						ProjectId: projectID, Host: r.Host, AppId: appID,
						Process: r.Process, Port: r.Port, Protocol: "http",
					})
					if err != nil {
						return err
					}
					routeByHost[r.Host] = created.GetRoute().GetId()
					report.Steps = append(report.Steps, dokployStep{Kind: "route", Name: r.Host, Detail: created.GetRoute().GetId()})
				}
			}
			if jsonOut {
				return writeJSON(env.Stdout, report)
			}
			if err := printPlan(); err != nil {
				return err
			}
			for _, s := range report.Steps {
				tag := "created"
				if s.Reused {
					tag = "reused"
				}
				detail := s.Detail
				if detail == "" {
					detail = "-"
				}
				if _, err := fmt.Fprintf(env.Stdout, "%s %s %s (%s)\n", s.Kind, s.Name, tag, detail); err != nil {
					return err
				}
			}
			if len(plan.Databases) > 0 {
				_, err := fmt.Fprintln(env.Stdout,
					"note: database data was not moved; it still lives on the dokploy host — restore it via Backup/Restore or a manual export/reload")
				if err != nil {
					return err
				}
			}
			_, err = fmt.Fprintln(env.Stdout, "migrations submitted: watch 'fleetly deployments list' for states")
			return err
		},
	}
}

// ensureDatabaseByName 按名 create-or-reuse（引擎一致性不校验——同名不同
// 引擎由服务端拒绝，诚实错误）。
func ensureDatabaseByName(ctx context.Context, c *sdk.Client, projectID, dbName, engine string) (string, bool, error) {
	list, err := c.Databases.ListDatabases(ctx, &structurev1.ListDatabasesRequest{ProjectId: projectID})
	if err != nil {
		return "", false, err
	}
	for _, d := range list.GetDatabases() {
		if d.GetName() == dbName {
			return d.GetId(), true, nil
		}
	}
	created, err := c.Databases.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: dbName, Engine: engine,
	})
	if err != nil {
		return "", false, err
	}
	return created.GetDatabase().GetId(), false, nil
}

// ensureAppByName 按名 create-or-reuse。
func ensureAppByName(ctx context.Context, c *sdk.Client, projectID, appName string) (string, bool, error) {
	list, err := c.Apps.ListApps(ctx, &structurev1.ListAppsRequest{ProjectId: projectID})
	if err != nil {
		return "", false, err
	}
	for _, a := range list.GetApps() {
		if a.GetName() == appName {
			return a.GetId(), true, nil
		}
	}
	created, err := c.Apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: projectID, Name: appName})
	if err != nil {
		return "", false, err
	}
	return created.GetApp().GetId(), false, nil
}

// deliveryDeployInput 是 deploy 的两形态输入（image 直投 / compose 文本）。
type deliveryDeployInput struct {
	AppID       string
	Image       string
	ComposeYAML string
	Env         map[string]string
	// Port 是 image 直投的端口声明（route 后端解析锚；compose 形态自带
	// 声明不适用）。
	Port int32
}

// deployViaAPI 走 Deploy RPC（compose 形态 env 已在解析期插值进文本——
// DeployRequest.env 与 compose 互斥）。
func deployViaAPI(ctx context.Context, c *sdk.Client, in *deliveryDeployInput) (string, error) {
	req := &deliveryv1.DeployRequest{AppId: in.AppID}
	if in.ComposeYAML != "" {
		req.ComposeYaml = in.ComposeYAML
	} else {
		req.Image = in.Image
		req.Env = in.Env
		req.Port = in.Port
	}
	dep, err := c.Deployments.Deploy(ctx, req)
	if err != nil {
		return "", err
	}
	return dep.GetDeployment().GetId(), nil
}
