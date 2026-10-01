package cmd

// Delivery 与运维动词：deploy/deployments/rollback/revisions/builds/
// routes/nodes（events/logs 随 C8 批接入）。

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lynx-go/commands"
	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func newDeployVerb() commands.Command {
	const name = "deploy"
	var app, image, composeFile, process, idemKey, commit, httpProbe string
	var tcpProbe int
	var supersede bool
	return &flaggedVerb{
		name: name, synopsis: "Deploy an app from an image or compose file",
		usage: "deploy --app APP_ID (--image REF | --compose-file PATH) [--idempotency-key K] [--supersede] [--http-probe PATH | --tcp-probe PORT]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.StringVar(&image, "image", "", "image reference (direct image deploy)")
			fs.StringVar(&composeFile, "compose-file", "", "compose file path (controlled subset)")
			fs.StringVar(&process, "process", "", "process name for image deploys (default web)")
			fs.StringVar(&idemKey, "idempotency-key", "", "admission idempotency key")
			fs.StringVar(&commit, "commit", "", "commit sha (webhook dedup anchor)")
			fs.BoolVar(&supersede, "supersede", false, "explicitly preempt any in-flight deployment")
			fs.StringVar(&httpProbe, "http-probe", "", "http health probe path for image deploys (absolute path, e.g. /healthz; probe port = --tcp-probe if set, else the first declared port, else 8080)")
			fs.IntVar(&tcpProbe, "tcp-probe", 0, "tcp health probe port for image deploys")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if app == "" {
				return usageErr(name, "--app is required")
			}
			if (image == "") == (composeFile == "") {
				return usageErr(name, "exactly one of --image or --compose-file is required")
			}
			if httpProbe != "" && tcpProbe != 0 {
				return usageErr(name, "--http-probe and --tcp-probe are mutually exclusive")
			}
			if (httpProbe != "" || tcpProbe != 0) && composeFile != "" {
				return usageErr(name, "--http-probe/--tcp-probe are for image deploys; compose declares probes via healthcheck.http_path/tcp_port")
			}
			compose := ""
			if composeFile != "" {
				data, err := os.ReadFile(composeFile) //nolint:gosec // 用户显式指定的输入路径
				if err != nil {
					return fmt.Errorf("read compose file: %w", err)
				}
				compose = string(data)
			}
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Deployments.Deploy(ctx, &deliveryv1.DeployRequest{
				AppId: app, Image: image, ComposeYaml: compose, ProcessName: process,
				IdempotencyKey: idemKey, CommitSha: commit, Supersede: supersede,
				HttpProbe: httpProbe, TcpProbe: int32(tcpProbe), //nolint:gosec // 端口域内
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetDeployment(), func() {
				d := resp.GetDeployment()
				_, _ = fmt.Fprintf(env.Stdout, "deployment %s %s (revision %s, generation %d)\n", d.GetId(), d.GetState(), d.GetToRevision(), d.GetGeneration())
			})
		},
	}
}

func newDeploymentsListVerb() commands.Command {
	const name = "list"
	var app string
	return &flaggedVerb{
		name: name, synopsis: "List deployments for an app", usage: "deployments list --app APP_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&app, "app", "", "app id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if app == "" {
				return usageErr(name, "--app is required")
			}
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: app})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tSTATE\tREVISION\tGENERATION\tUPDATED")
				for _, d := range resp.GetDeployments() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%d\t%s\n", d.GetId(), d.GetState(), d.GetToRevision(), d.GetGeneration(), d.GetUpdatedAt())
				}
			})
		},
	}
}

func newRollbackVerb() commands.Command {
	const name = "rollback"
	var app, to string
	return &flaggedVerb{
		name: name, synopsis: "Roll back an app by replaying a revision (first-class verb)",
		usage: "rollback --app APP_ID [--to REVISION_ID] (default: last successful baseline)",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.StringVar(&to, "to", "", "target revision id (default: last succeeded baseline)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if app == "" {
				return usageErr(name, "--app is required")
			}
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Deployments.Rollback(ctx, &deliveryv1.RollbackRequest{AppId: app, ToRevision: to})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetDeployment(), func() {
				d := resp.GetDeployment()
				_, _ = fmt.Fprintf(env.Stdout, "rolling back via deployment %s (target %s)\n", d.GetId(), d.GetToRevision())
			})
		},
	}
}

func newRevisionsListVerb() commands.Command {
	const name = "list"
	var app string
	return &flaggedVerb{
		name: name, synopsis: "List frozen revisions for an app", usage: "revisions list --app APP_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&app, "app", "", "app id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if app == "" {
				return usageErr(name, "--app is required")
			}
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Revisions.ListRevisions(ctx, &deliveryv1.ListRevisionsRequest{AppId: app})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "SEQ\tID\tDIGEST\tCREATED")
				for _, r := range resp.GetRevisions() {
					_, _ = fmt.Fprintf(env.Stdout, "R%d\t%s\t%s\t%s\n", r.GetSeq(), r.GetId(), r.GetDigest(), r.GetCreatedAt())
				}
			})
		},
	}
}

func newRevisionsDiffVerb() commands.Command {
	const name = "diff"
	var app string
	var from, to int64
	return &flaggedVerb{
		name: name, synopsis: "Field-level diff between two revisions",
		usage: "revisions diff --app APP_ID --from SEQ --to SEQ",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.Int64Var(&from, "from", 0, "from revision seq (required)")
			fs.Int64Var(&to, "to", 0, "to revision seq (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if app == "" || from == 0 || to == 0 {
				return usageErr(name, "--app, --from and --to are required")
			}
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Revisions.DiffRevisions(ctx, &deliveryv1.DiffRevisionsRequest{AppId: app, FromSeq: from, ToSeq: to})
			if err != nil {
				return err
			}
			if err := renderOut(env, jsonOut, resp, func() {
				if len(resp.GetEntries()) == 0 {
					_, _ = fmt.Fprintln(env.Stdout, "no differences")
					return
				}
				_, _ = fmt.Fprintln(env.Stdout, "PATH\tOLD\tNEW")
				for _, e := range resp.GetEntries() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\n", e.GetPath(), e.GetOldValue(), e.GetNewValue())
				}
			}); err != nil {
				return err
			}
			// 有变化 → 退出码 2（diff 类动词机器契约；stdout 照常是 diff 面）。
			if len(resp.GetEntries()) > 0 {
				return errChanges
			}
			return nil
		},
	}
}

func newBuildsListVerb() commands.Command {
	const name = "list"
	var app string
	return &flaggedVerb{
		name: name, synopsis: "List builds for an app", usage: "builds list --app APP_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&app, "app", "", "app id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if app == "" {
				return usageErr(name, "--app is required")
			}
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Builds.ListBuilds(ctx, &deliveryv1.ListBuildsRequest{AppId: app})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tSTATE\tDIGEST\tCREATED")
				for _, b := range resp.GetBuilds() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\n", b.GetId(), b.GetState(), b.GetDigest(), b.GetCreatedAt())
				}
			})
		},
	}
}

// builds logs（B4）：读构建日志（最近缓冲 + follow 续流至终态；持久化
// 检索 N2——诚实边界同 F0.25）。
func newBuildsLogsVerb() commands.Command {
	const name = "logs"
	var build string
	var follow bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Stream build logs (recent buffer; --follow keeps streaming until the build finishes)",
		usage:    "builds logs --build BUILD_ID [--follow]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&build, "build", "", "build id (required)")
			fs.BoolVar(&follow, "follow", false, "keep streaming new output")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if build == "" {
				return usageErr(name, "--build is required")
			}
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			stream, err := c.Builds.StreamBuildLogs(ctx, &deliveryv1.StreamBuildLogsRequest{
				BuildId: build, Follow: follow,
			})
			if err != nil {
				return err
			}
			compact := func(m proto.Message) (string, error) {
				data, err := protoJSONMarshal.Marshal(m)
				if err != nil {
					return "", err
				}
				var buf bytes.Buffer
				if err := json.Compact(&buf, data); err != nil {
					return "", err
				}
				return buf.String(), nil
			}
			for {
				frame, err := stream.Recv()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return err
				}
				if jsonOut {
					line, err := compact(frame)
					if err != nil {
						return err
					}
					_, _ = fmt.Fprintln(env.Stdout, line)
					continue
				}
				_, _ = fmt.Fprintf(env.Stdout, "%s %s\n", frame.GetTime(), string(frame.GetLine()))
			}
		},
	}
}

func newRoutesCreateVerb() commands.Command {
	const name = "create"
	var project, host, path, app, process, protocol, tlsMode string
	var port int
	return &flaggedVerb{
		name: name, synopsis: "Create a route (host/path -> app process port)",
		usage: "routes create --project P --host H --app A --process NAME --port N [--protocol http|h2c|tcp] [--tls auto|none]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.StringVar(&host, "host", "", "hostname (required)")
			fs.StringVar(&path, "path", "", "path prefix (default /)")
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.StringVar(&process, "process", "", "process name (required)")
			fs.IntVar(&port, "port", 0, "target port (required)")
			fs.StringVar(&protocol, "protocol", "http", "backend protocol: http | h2c | tcp")
			fs.StringVar(&tlsMode, "tls", "auto", "tls mode: auto | none")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if project == "" || host == "" || app == "" || process == "" || port == 0 {
				return usageErr(name, "--project, --host, --app, --process and --port are required")
			}
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
				ProjectId: project, Host: host, Path: path, AppId: app, Process: process,
				Port:     int32(port), //nolint:gosec // 端口域内
				Protocol: protocol, TlsMode: tlsMode,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetRoute(), func() {
				r := resp.GetRoute()
				_, _ = fmt.Fprintf(env.Stdout, "created route %s -> %s:%d (%s)\n", r.GetHost(), r.GetProcess(), r.GetPort(), r.GetProtocol())
			})
		},
	}
}

func newRoutesListVerb() commands.Command {
	const name = "list"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "List routes", usage: "routes list [--project PROJECT_ID]",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&project, "project", "", "filter by project") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Routes.ListRoutes(ctx, &edgev1.ListRoutesRequest{ProjectId: project})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tHOST\tPATH\tAPP\tPROCESS\tPORT\tPROTO\tTLS")
				for _, r := range resp.GetRoutes() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
						r.GetId(), r.GetHost(), r.GetPath(), r.GetAppId(), r.GetProcess(), r.GetPort(), r.GetProtocol(), r.GetTlsMode())
				}
			})
		},
	}
}

func newNodesListVerb() commands.Command {
	return &flaggedVerb{
		name: "list", synopsis: "List observed cluster nodes", usage: "nodes list",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Nodes.ListNodes(ctx, &runtimev1.ListNodesRequest{})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "PLATFORM_ID\tROLE\tAVAILABLE\tHOSTNAME\tLAST_SEEN")
				for _, n := range resp.GetNodes() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%t\t%s\t%s\n", n.GetPlatformId(), n.GetRole(), n.GetAvailable(), n.GetHostname(), n.GetLastSeenAt())
				}
			})
		},
	}
}

func newNodesEnrollVerb() commands.Command {
	const name = "enroll"
	var rotate bool
	return &flaggedVerb{
		name: name, synopsis: "Print the worker join command (zero platform install on workers)",
		usage: "nodes enroll [--rotate]",
		setFlags: func(fs *flag.FlagSet) {
			fs.BoolVar(&rotate, "rotate", false, "invalidate all existing join tokens first (leak response)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Nodes.EnrollNode(ctx, &runtimev1.EnrollNodeRequest{Rotate: rotate})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, resp.GetJoinCommand())
			})
		},
	}
}

// newNodesAdminVerb 构造节点运维动词（drain/cordon/uncordon；平台节点 ID
// 为锚，幂等可重放——结果即动作本身，无数据面返回）。
func newNodesAdminVerb(name, past, synopsis string, call func(ctx context.Context, c *fleetly.Client, nodeID string) (proto.Message, error)) commands.Command {
	var nodeID string
	return &flaggedVerb{
		name: name, synopsis: synopsis,
		usage: "nodes " + name + " --node ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&nodeID, "node", "", "platform node id (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if nodeID == "" {
				return usageErr(name, "--node is required")
			}
			ctx, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := call(ctx, c, nodeID)
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintf(env.Stdout, "%s node %s\n", past, nodeID)
			})
		},
	}
}

func newNodesDrainVerb() commands.Command {
	return newNodesAdminVerb("drain", "drained", "Set a node to drain (workloads reschedule off it)",
		func(ctx context.Context, c *fleetly.Client, nodeID string) (proto.Message, error) {
			return c.Nodes.DrainNode(ctx, &runtimev1.DrainNodeRequest{NodeId: nodeID})
		})
}

func newNodesCordonVerb() commands.Command {
	return newNodesAdminVerb("cordon", "cordoned", "Cordon a node (no new placements, existing workloads stay)",
		func(ctx context.Context, c *fleetly.Client, nodeID string) (proto.Message, error) {
			return c.Nodes.CordonNode(ctx, &runtimev1.CordonNodeRequest{NodeId: nodeID})
		})
}

func newNodesUncordonVerb() commands.Command {
	return newNodesAdminVerb("uncordon", "uncordoned", "Uncordon a node (resume placements)",
		func(ctx context.Context, c *fleetly.Client, nodeID string) (proto.Message, error) {
			return c.Nodes.UncordonNode(ctx, &runtimev1.UncordonNodeRequest{NodeId: nodeID})
		})
}
