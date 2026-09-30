package cmd

// fleetly quickstart（F0.3）：样例应用一条龙——project/app → 镜像部署 →
// sslip.io Route（host 自动拼：--addr 的非环回 IP，否则 127.0.0.1）→ 等待
// succeeded → 输出访问 URL。TLS 默认 none（h2c/http 立即可用）；--tls auto
// 走 LE ACME（真机公网 DNS+80/443 才有真证书——staging CA 默认，真机验收
// 随 F0.18 批）。

import (
	"context"
	"flag"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/lynx-go/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
)

// quickstartReport 是 --json 形态。
type quickstartReport struct {
	ProjectID    string `json:"project_id"`
	AppID        string `json:"app_id"`
	DeploymentID string `json:"deployment_id"`
	RouteID      string `json:"route_id"`
	Host         string `json:"host"`
	URL          string `json:"url"`
	State        string `json:"state"`
}

// sslipHost 从连接地址拼 sslip.io 域名（非环回 IP 用之；否则 127.0.0.1）。
func sslipHost(addr, name string) string {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if host == "" || host == "localhost" {
		host = "127.0.0.1"
	}
	if net.ParseIP(host) == nil {
		// 域名形态的地址：不再套 sslip（用户已有 DNS 面）。
		return name + "." + host
	}
	return name + "." + host + ".sslip.io"
}

func newQuickstartVerb() commands.Command {
	const name = "quickstart"
	var appName, image, host, tlsMode string
	var port int
	var noWait bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Deploy the sample app with an sslip.io route end to end",
		usage:    "quickstart [--name demo] [--image nginx:1.27] [--port 80] [--host HOST] [--tls none|auto] [--no-wait]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&appName, "name", "demo", "app and host label (default \"demo\")")
			fs.StringVar(&image, "image", "nginx:1.27", "sample image to deploy")
			fs.IntVar(&port, "port", 80, "container port the route forwards to")
			fs.StringVar(&host, "host", "", "route host (default: <name>.<server-ip>.sslip.io)")
			fs.StringVar(&tlsMode, "tls", "none", "route TLS mode: none (default) or auto (ACME; needs public DNS)")
			fs.BoolVar(&noWait, "no-wait", false, "submit and print without waiting for the deployment")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if tlsMode != "none" && tlsMode != "auto" {
				return usageErr(name, "--tls must be none or auto")
			}
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			// sslip host 的 IP 锚取自连接地址（与 dialFromEnv 同一解析序：
			// flag > env > 默认；凭据文件的 addr 不参与）。
			serverAddr := envOr(envAddr, defaultAddr)

			// 1. project（重跑幂等：存在即复用）。
			var projectID string
			plist, err := c.Projects.ListProjects(ctx, &structurev1.ListProjectsRequest{})
			if err != nil {
				return err
			}
			for _, p := range plist.GetProjects() {
				if p.GetName() == "quickstart" {
					projectID = p.GetId()
				}
			}
			if projectID == "" {
				p, err := c.Projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "quickstart"})
				if err != nil {
					return err
				}
				projectID = p.GetProject().GetId()
			}

			// 2. app（同款幂等）。
			var appID string
			alist, err := c.Apps.ListApps(ctx, &structurev1.ListAppsRequest{ProjectId: projectID})
			if err != nil {
				return err
			}
			for _, a := range alist.GetApps() {
				if a.GetName() == appName {
					appID = a.GetId()
				}
			}
			if appID == "" {
				a, err := c.Apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: projectID, Name: appName})
				if err != nil {
					return err
				}
				appID = a.GetApp().GetId()
			}

			// 3. 部署样例镜像（compose 形态：Route 后端解析依赖端口声明
			// 落 fleetly.ports 标注——镜像直投无端口声明面，Edge 后端将
			// 无从解析；compose 受控子集带 ports 声明）。
			compose := fmt.Sprintf("services:\n  web:\n    image: %s\n    ports:\n      - \"%d\"\n", image, port)
			dep, err := c.Deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, ComposeYaml: compose})
			if err != nil {
				return err
			}

			// 4. Route（sslip.io host 默认拼）。
			if host == "" {
				host = sslipHost(serverAddr, appName)
			}
			route, err := c.Routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
				ProjectId: projectID, Host: host, AppId: appID,
				Process: "web", Port: int32(port), Protocol: "http", TlsMode: tlsMode, //nolint:gosec // 端口域内
			})
			if err != nil {
				return err
			}

			// 5. 等待 succeeded（轮询部署状态；终态即止）。
			state := dep.GetDeployment().GetState()
			if !noWait {
				for i := 0; i < 120; i++ {
					list, lerr := c.Deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
					if lerr != nil {
						return lerr
					}
					if len(list.GetDeployments()) > 0 {
						state = list.GetDeployments()[0].GetState()
						if state == "succeeded" || state == "failed" || state == "cancelled" || state == "superseded" {
							break
						}
					}
					time.Sleep(1 * time.Second)
				}
			}

			scheme := "http"
			if tlsMode == "auto" {
				scheme = "https"
			}
			report := quickstartReport{
				ProjectID: projectID, AppID: appID,
				DeploymentID: dep.GetDeployment().GetId(),
				RouteID:      route.GetRoute().GetId(),
				Host:         host, URL: scheme + "://" + host,
				State: state,
			}
			if jsonOut {
				return writeJSON(env.Stdout, report)
			}
			_, err = fmt.Fprintf(env.Stdout,
				"app %s deployed from %s (deployment %s, state %s)\nroute %s -> %s:%d (tls %s)\nopen %s\n",
				appName, image, report.DeploymentID, state, host, appName, port, tlsMode, report.URL)
			if err != nil {
				return err
			}
			if strings.HasPrefix(report.Host, appName+".127.0.0.1.") {
				_, _ = fmt.Fprintln(env.Stdout, "note: the sslip host resolves to 127.0.0.1 — reachable from this machine only; pass --addr <public-ip>:9080 for a public host")
			}
			return nil
		},
	}
}
