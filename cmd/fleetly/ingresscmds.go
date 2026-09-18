package main

// fleetly ingress status 命令（T2.15/T2.16）：入口链三面状态——
//  1. Traefik 服务实况（Swarm service fleetly-ingress，只读 inspect）；
//  2. 控制面配置端点健康（/healthz 无鉴权 + /configs 带 token 鉴权核验；
//     token 缺失/错误时如实报告 401 路径）；
//  3. 证书清单（domains 台账 cert 列）与证书目录对照。
//
// 状态库直连 + Docker 底座只读（substrate.Client 复用；无写路径）。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/internal/ingress"
	"github.com/fleetlyrun/fleetly/internal/substrate"
)

// traefikView 是 Traefik 服务实况投影。
type traefikView struct {
	Exists bool   `json:"exists"`
	Image  string `json:"image,omitempty"`
	Args   int    `json:"static_args"`
	Global bool   `json:"global"`
	Err    string `json:"error,omitempty"`
}

// ingressCmd 是外层动词 `ingress`：分发 status。
type ingressCmd struct {
	sub *commands.App
}

func newIngressCmd() *ingressCmd {
	sub := commands.New()
	sub.Register(&ingressStatusCmd{})
	sub.VerbTitle = "ingress subcommands:"
	return &ingressCmd{sub: sub}
}

func (c *ingressCmd) Name() string { return "ingress" }
func (c *ingressCmd) Synopsis() string {
	return "ingress chain (traefik deployment, config endpoint, certificates)"
}
func (c *ingressCmd) Usage() string { return "ingress <status> [flags]" }

func (c *ingressCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (status)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// endpointView 是配置端点两面探测结果。
type endpointView struct {
	Healthz string `json:"healthz"`
	Auth    string `json:"auth"`
}

// certLedgerRow 是台账证书行的展示投影。
type certLedgerRow struct {
	App          string `json:"app"`
	Domain       string `json:"domain"`
	CertSHA256   string `json:"cert_sha256,omitempty"`
	CertNotAfter string `json:"cert_not_after,omitempty"`
}

// ingressStatusCmd 实现 `fleetly ingress status [--json]`。
type ingressStatusCmd struct {
	db        string
	endpoint  string
	tokenFile string
	certDir   string
	dockerHst string
	jsonOut   bool
}

func (c *ingressStatusCmd) Name() string { return "status" }
func (c *ingressStatusCmd) Synopsis() string {
	return "ingress chain status (traefik service / config endpoint / certificates)"
}
func (c *ingressStatusCmd) Usage() string {
	return "ingress status [--db <path>] [--endpoint <url>] [--token-file <path>] [--cert-dir <dir>] [--docker-host <h>] [--json]"
}

func (c *ingressStatusCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.db, "db", defaultDBPath, "state db path")
	fs.StringVar(&c.endpoint, "endpoint", "http://127.0.0.1:8422", "control-plane config endpoint base URL")
	fs.StringVar(&c.tokenFile, "token-file", "fleetly-ingress.token", "config endpoint token file")
	fs.StringVar(&c.certDir, "cert-dir", "fleetly-certs", "certificate store dir")
	fs.StringVar(&c.dockerHst, "docker-host", "", "docker host (empty = DOCKER_HOST/local default)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *ingressStatusCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}

	// ① Traefik 服务实况（只读；docker 不可达如实标注）。
	tv := traefikView{}
	sc, err := substrate.NewClient(c.dockerHst)
	if err != nil {
		tv.Err = err.Error()
	} else {
		ins, ierr := sc.ServiceInspect(ctx, ingress.IngressServiceName)
		if ierr != nil {
			tv.Err = ierr.Error()
		} else {
			tv.Exists = true
			tv.Image = ins.Image
			tv.Args = len(ins.Command)
			tv.Global = ins.Global
		}
		_ = sc.Close()
	}

	// ② 配置端点：/healthz + /configs 鉴权核验（401 = token 缺失/错误，
	// 如实报告；200 = 鉴权通过且返回合法 JSON）。
	ev := endpointView{Healthz: "unreachable", Auth: "unreachable"}
	client := &http.Client{Timeout: 3 * time.Second}
	if resp, err := client.Get(strings.TrimRight(c.endpoint, "/") + "/healthz"); err == nil {
		_ = resp.Body.Close()
		ev.Healthz = resp.Status
	}
	tokenRaw, tokErr := os.ReadFile(c.tokenFile)
	token := strings.TrimSpace(string(tokenRaw))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.endpoint, "/")+"/configs", nil)
	if tokErr == nil && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if resp, err := client.Do(req); err == nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			var probe map[string]any
			if json.Unmarshal(body, &probe) == nil {
				ev.Auth = "200 (authorized)"
			} else {
				ev.Auth = "200 (non-JSON body?)"
			}
		default:
			ev.Auth = resp.Status
		}
	} else if tokErr != nil {
		ev.Auth = "token file unreadable: " + c.tokenFile
	}

	// ③ 证书清单：台账 cert 列 + 证书目录对照。
	st, done, err := openStore(c.db)
	if err != nil {
		return err
	}
	defer done()
	rows, err := st.ListAllDomains(ctx)
	if err != nil {
		return err
	}
	certs := []certLedgerRow{}
	nameByAppID := map[string]string{}
	for _, r := range rows {
		if r.CertSHA256 == "" {
			continue
		}
		if _, ok := nameByAppID[r.AppID]; !ok {
			appRow, err := st.GetAppByID(ctx, r.AppID)
			if err != nil {
				nameByAppID[r.AppID] = r.AppID // 已删除应用：回退显示 ID
			} else {
				nameByAppID[r.AppID] = appRow.Name
			}
		}
		certs = append(certs, certLedgerRow{
			App:          nameByAppID[r.AppID],
			Domain:       r.Domain,
			CertSHA256:   r.CertSHA256,
			CertNotAfter: r.CertNotAfter.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	apps, certDirErr := listCertApps(c.certDir)

	if c.jsonOut {
		out := map[string]any{
			"traefik":         tv,
			"config_endpoint": ev,
			"certificates":    certs,
			"cert_dir":        map[string]any{"path": c.certDir, "apps": apps},
		}
		if certDirErr != nil {
			out["cert_dir_error"] = certDirErr.Error()
		}
		return writeJSON(env, out)
	}
	var b strings.Builder
	b.WriteString("ingress status\n")
	if tv.Exists {
		fmt.Fprintf(&b, "  traefik: %s (global=%t, static args=%d)\n", tv.Image, tv.Global, tv.Args)
	} else {
		msg := tv.Err
		if msg == "" {
			msg = "not deployed"
		}
		fmt.Fprintf(&b, "  traefik: %s\n", msg)
	}
	fmt.Fprintf(&b, "  config endpoint: healthz=%s auth=%s\n", ev.Healthz, ev.Auth)
	fmt.Fprintf(&b, "  certificates (ledger): %d\n", len(certs))
	for _, ct := range certs {
		fmt.Fprintf(&b, "    %s: sha256:%s… expires %s\n", ct.Domain, short8(ct.CertSHA256), ct.CertNotAfter)
	}
	if certDirErr != nil {
		fmt.Fprintf(&b, "  cert dir %s: %v\n", c.certDir, certDirErr)
	} else {
		fmt.Fprintf(&b, "  cert dir apps: %s\n", strings.Join(apps, ", "))
	}
	_, err = fmt.Fprint(env.Stdout, b.String())
	return err
}

// listCertApps 读证书目录 app 清单（meta 索引；目录缺失 = 空清单非错误）。
func listCertApps(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	apps := []string{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			apps = append(apps, strings.TrimSuffix(e.Name(), ".meta.json"))
		}
	}
	return apps, nil
}

// 编译期断言：ingress 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &ingressCmd{}
	_ commands.Command = &ingressStatusCmd{}
	_ commands.Flagged = &ingressStatusCmd{}
)
