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
			// 等待是流式长等待：豁免请求级 deadline（F-15 收编后等待不再受
			// 120s 请求 deadline 腰斩）。
			var dialOpts []dialOption
			if !noWait {
				dialOpts = append(dialOpts, noDeadline())
			}
			ctx, cancel, c, err := dialFromEnv(ctx, dialOpts...)
			if err != nil {
				return err
			}
			defer cancel()
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

			// 3. Project 网络（幂等：存在即复用）。受管 Edge 挂全部活跃
			// Project 网络（B1）——后端与 Edge 在同一 overlay 才互通，
			// quickstart 一条龙必须把网络实体建出来。
			nlist, err := c.Networks.ListNetworks(ctx, &structurev1.ListNetworksRequest{ProjectId: projectID})
			if err != nil {
				return err
			}
			hasDefault := false
			for _, n := range nlist.GetNetworks() {
				if n.GetName() == "default" {
					hasDefault = true
				}
			}
			if !hasDefault {
				if _, err := c.Networks.CreateNetwork(ctx, &structurev1.CreateNetworkRequest{
					ProjectId: projectID, Name: "default",
				}); err != nil {
					return err
				}
			}

			// 4. 部署样例镜像（compose 形态：Route 后端解析依赖端口声明
			// 落 fleetly.ports 标注——镜像直投无端口声明面，Edge 后端将
			// 无从解析；networks 挂 Project 网让后端与受管 Edge 同网互通）。
			compose := fmt.Sprintf("services:\n  web:\n    image: %s\n    ports:\n      - \"%d\"\n    networks:\n      - default\n", image, port)
			dep, err := c.Deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, ComposeYaml: compose})
			if err != nil {
				return err
			}

			// 5. Route（sslip.io host 默认拼；幂等复用：同 host 既有 Route
			// 直接复用——重跑 quickstart 不因昨天的路由而 E_ALREADY_EXISTS）。
			if host == "" {
				host = sslipHost(serverAddr, appName)
			}
			rlist, err := c.Routes.ListRoutes(ctx, &edgev1.ListRoutesRequest{})
			if err != nil {
				return err
			}
			var route *edgev1.Route
			for _, r := range rlist.GetRoutes() {
				if r.GetHost() == host {
					route = r // 同 host 既有 Route 直接复用（列表携带全量字段）
				}
			}
			if route == nil {
				created, err := c.Routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
					ProjectId: projectID, Host: host, AppId: appID,
					Process: "web", Port: int32(port), Protocol: "http", TlsMode: tlsMode, //nolint:gosec // 端口域内
				})
				if err != nil {
					return err
				}
				route = created.GetRoute()
			}

			// 6. 等待 succeeded（F-15 收编：等待原语取代私有 120×1s 轮询——
			// 帧只跟自己部署的那条，不再是"该 App 最新一条"的口径漂移）。
			state := dep.GetDeployment().GetState()
			if !noWait {
				state, err = waitDeploymentFrames(ctx, c, dep.GetDeployment().GetId(), nil)
				if err != nil {
					return err
				}
			}

			scheme := "http"
			if tlsMode == "auto" {
				scheme = "https"
			}
			report := quickstartReport{
				ProjectID: projectID, AppID: appID,
				DeploymentID: dep.GetDeployment().GetId(),
				RouteID:      route.GetId(),
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
