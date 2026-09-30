package cmd

// Delivery 与运维动词：deploy/deployments/rollback/revisions/builds/
// routes/nodes（events/logs 随 C8 批接入）。

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/lynx-go/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
)

func newDeployVerb() commands.Command {
	const name = "deploy"
	var app, image, composeFile, process, idemKey, commit string
	var supersede bool
	return &flaggedVerb{
		name: name, synopsis: "Deploy an app from an image or compose file",
		usage: "deploy --app APP_ID (--image REF | --compose-file PATH) [--idempotency-key K] [--supersede]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.StringVar(&image, "image", "", "image reference (direct image deploy)")
			fs.StringVar(&composeFile, "compose-file", "", "compose file path (controlled subset)")
			fs.StringVar(&process, "process", "", "process name for image deploys (default web)")
			fs.StringVar(&idemKey, "idempotency-key", "", "admission idempotency key")
			fs.StringVar(&commit, "commit", "", "commit sha (webhook dedup anchor)")
			fs.BoolVar(&supersede, "supersede", false, "explicitly preempt any in-flight deployment")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if app == "" {
				return usageErr(name, "--app is required")
			}
			if (image == "") == (composeFile == "") {
				return usageErr(name, "exactly one of --image or --compose-file is required")
			}
			compose := ""
			if composeFile != "" {
				data, err := os.ReadFile(composeFile) //nolint:gosec // 用户显式指定的输入路径
				if err != nil {
					return fmt.Errorf("read compose file: %w", err)
				}
				compose = string(data)
			}
			c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Deployments.Deploy(ctx, &deliveryv1.DeployRequest{
				AppId: app, Image: image, ComposeYaml: compose, ProcessName: process,
				IdempotencyKey: idemKey, CommitSha: commit, Supersede: supersede,
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
			c, err := dialFromEnv(ctx)
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
			c, err := dialFromEnv(ctx)
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
			c, err := dialFromEnv(ctx)
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
			c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Revisions.DiffRevisions(ctx, &deliveryv1.DiffRevisionsRequest{AppId: app, FromSeq: from, ToSeq: to})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				if len(resp.GetEntries()) == 0 {
					_, _ = fmt.Fprintln(env.Stdout, "no differences")
					return
				}
				_, _ = fmt.Fprintln(env.Stdout, "PATH\tOLD\tNEW")
				for _, e := range resp.GetEntries() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\n", e.GetPath(), e.GetOldValue(), e.GetNewValue())
				}
			})
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
			c, err := dialFromEnv(ctx)
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
			c, err := dialFromEnv(ctx)
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
			c, err := dialFromEnv(ctx)
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
			c, err := dialFromEnv(ctx)
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
	return &flaggedVerb{
		name: "enroll", synopsis: "Print the worker join command (zero platform install on workers)",
		usage: "nodes enroll",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Nodes.EnrollNode(ctx, &runtimev1.EnrollNodeRequest{})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, resp.GetJoinCommand())
			})
		},
	}
}
