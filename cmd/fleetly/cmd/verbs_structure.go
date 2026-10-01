package cmd

// Structure 上下文动词（projects/apps/secrets/configs/volumes/networks）。
// 全部 --json 双形态（golden 钉死）；人类形态为稳定的行式输出。旗标经
// 闭包变量捕获（commands.Flagged 契约：SetFlags 声明、Run 读字段）。

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lynx-go/commands"
	"google.golang.org/protobuf/proto"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
)

func usageErr(verb, msg string) error {
	return &commands.UsageError{Usage: verb, Err: fmt.Errorf("%s", msg)}
}

// renderOut 是渲染单点分支：机器形态 protojson；人类形态走 human 闭包。
func renderOut(env *commands.Environment, jsonOut bool, msg proto.Message, human func()) error {
	if jsonOut {
		return writeProtoJSON(env.Stdout, msg)
	}
	human()
	return nil
}

// valueOrStdin 取旗标值或读进程 stdin 全量（Environment 无 stdin 注入面；
// os.Stdin 直读）。
func valueOrStdin(value string, _ *commands.Environment) (string, error) {
	if value != "" {
		return value, nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func newProjectsCreateVerb() commands.Command {
	const name = "create"
	var team string
	var idem idemKeyFlag
	return &flaggedVerb{
		name: name, synopsis: "Create a project", usage: "projects create NAME [--team TEAM]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&team, "team", "default", "owning team")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: args[0], TeamId: team})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetProject(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "created project %s (id %s)\n", resp.GetProject().GetName(), resp.GetProject().GetId())
			})
		},
	}
}

func newProjectsListVerb() commands.Command {
	return &flaggedVerb{
		name: "list", synopsis: "List projects", usage: "projects list",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Projects.ListProjects(ctx, &structurev1.ListProjectsRequest{})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tNAME\tTEAM\tCREATED")
				for _, p := range resp.GetProjects() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\n", p.GetId(), p.GetName(), p.GetTeamId(), p.GetCreatedAt())
				}
			})
		},
	}
}

// projects delete（Q-15）：项目下有未删 App 即拒（E_CONFLICT，先删
// App）；Project 级材料（Secret/Config/Volume/Network）随各自生命周期
// 不级联。
func newProjectsDeleteVerb() commands.Command {
	const name = "delete"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Delete a project (refuses while it still holds apps)",
		usage:    "projects delete --project PROJECT_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&project, "project", "", "project id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if project == "" {
				return usageErr(name, "--project is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: project})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintf(env.Stdout, "deleted project %s\n", project)
			})
		},
	}
}

func newAppsCreateVerb() commands.Command {
	const name = "create"
	var project string
	var idem idemKeyFlag
	return &flaggedVerb{
		name: name, synopsis: "Create an app in a project", usage: "apps create --project PROJECT_ID NAME",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "owning project id (required)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if project == "" {
				return usageErr(name, "--project is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: project, Name: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetApp(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "created app %s (id %s)\n", resp.GetApp().GetName(), resp.GetApp().GetId())
			})
		},
	}
}

func newAppsListVerb() commands.Command {
	const name = "list"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "List apps in a project", usage: "apps list --project PROJECT_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&project, "project", "", "project id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if project == "" {
				return usageErr(name, "--project is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Apps.ListApps(ctx, &structurev1.ListAppsRequest{ProjectId: project})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tNAME\tCREATED")
				for _, a := range resp.GetApps() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\n", a.GetId(), a.GetName(), a.GetCreatedAt())
				}
			})
		},
	}
}

// apps delete（ADR-0023）：收口删除——拆载体 + 撤路由 + tombstone；活跃
// 部署在（E_CONFLICT）时拒绝（先 cancel 或等终态）。
func newAppsDeleteVerb() commands.Command {
	const name = "delete"
	var app string
	return &flaggedVerb{
		name: name, synopsis: "Delete an app (tears down carriers and routes; refuses while deployments are active)",
		usage:    "apps delete --app APP_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&app, "app", "", "app id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if app == "" {
				return usageErr(name, "--app is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: app})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintf(env.Stdout, "deleted app %s (carriers and routes torn down)\n", app)
			})
		},
	}
}

func newSecretsPutVerb() commands.Command {
	const name = "put"
	var project, value string
	var idem idemKeyFlag
	return &flaggedVerb{
		name: name, synopsis: "Create or update a secret (value from --value or stdin)",
		usage: "secrets put --project PROJECT_ID NAME [--value VALUE]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.StringVar(&value, "value", "", "secret value (empty = read stdin)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if project == "" {
				return usageErr(name, "--project is required")
			}
			v, err := valueOrStdin(value, env)
			if err != nil {
				return err
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Secrets.PutSecret(ctx, &structurev1.PutSecretRequest{ProjectId: project, Name: args[0], Value: v})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetSecret(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "stored secret %s (fingerprint %s)\n", resp.GetSecret().GetName(), resp.GetSecret().GetFingerprint())
			})
		},
	}
}

func newSecretsListVerb() commands.Command {
	const name = "list"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "List secret fingerprints (values are never returned)",
		usage:    "secrets list --project PROJECT_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&project, "project", "", "project id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if project == "" {
				return usageErr(name, "--project is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Secrets.ListSecrets(ctx, &structurev1.ListSecretsRequest{ProjectId: project})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "NAME\tFINGERPRINT\tUPDATED")
				for _, s := range resp.GetSecrets() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\n", s.GetName(), s.GetFingerprint(), s.GetUpdatedAt())
				}
			})
		},
	}
}

func newConfigsPutVerb() commands.Command {
	const name = "put"
	var project, value string
	var idem idemKeyFlag
	return &flaggedVerb{
		name: name, synopsis: "Write a new config version from --value or stdin",
		usage: "configs put --project PROJECT_ID NAME [--value CONTENT]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.StringVar(&value, "value", "", "config content (empty = read stdin)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if project == "" {
				return usageErr(name, "--project is required")
			}
			v, err := valueOrStdin(value, env)
			if err != nil {
				return err
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Configs.PutConfig(ctx, &structurev1.PutConfigRequest{ProjectId: project, Name: args[0], Content: v})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetConfig(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "stored config %s (version %d)\n", resp.GetConfig().GetName(), resp.GetConfig().GetVersion())
			})
		},
	}
}

func newConfigsListVerb() commands.Command {
	const name = "list"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "List config latest versions", usage: "configs list --project PROJECT_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&project, "project", "", "project id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if project == "" {
				return usageErr(name, "--project is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Configs.ListConfigs(ctx, &structurev1.ListConfigsRequest{ProjectId: project})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "NAME\tVERSION\tCREATED")
				for _, cf := range resp.GetConfigs() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%d\t%s\n", cf.GetName(), cf.GetVersion(), cf.GetCreatedAt())
				}
			})
		},
	}
}

func newVolumesCreateVerb() commands.Command {
	const name = "create"
	var project, node string
	var idem idemKeyFlag
	return &flaggedVerb{
		name: name, synopsis: "Create a volume (pinned on first mount by default)",
		usage: "volumes create --project PROJECT_ID NAME [--node PLATFORM_NODE_ID]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.StringVar(&node, "node", "", "pin to a platform node id (default: pinned on first mount)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if project == "" {
				return usageErr(name, "--project is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Volumes.CreateVolume(ctx, &structurev1.CreateVolumeRequest{
				ProjectId: project, Name: args[0], PinnedNodeId: node,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetVolume(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "created volume %s (id %s)\n", resp.GetVolume().GetName(), resp.GetVolume().GetId())
			})
		},
	}
}

func newNetworksCreateVerb() commands.Command {
	const name = "create"
	var project string
	var egressNone bool
	var idem idemKeyFlag
	return &flaggedVerb{
		name: name, synopsis: "Create a project network (egress:none is weak isolation on swarm v1)",
		usage: "networks create --project PROJECT_ID NAME [--egress-none]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.BoolVar(&egressNone, "egress-none", false, "declare egress:none (weak isolation on swarm: outbound NOT blocked)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if project == "" {
				return usageErr(name, "--project is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Networks.CreateNetwork(ctx, &structurev1.CreateNetworkRequest{
				ProjectId: project, Name: args[0], EgressNone: egressNone,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetNetwork(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "created network %s (id %s)\n", resp.GetNetwork().GetName(), resp.GetNetwork().GetId())
			})
		},
	}
}

// ---- networks peers（跨 Project 挂靠声明，ADR-0013 附录 A） ----

func newNetworksDeclareVerb() commands.Command {
	const name = "declare"
	var network, project string
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Declare a cross-project peer attachment (pending until the receiver approves)",
		usage:    "networks declare --network NETWORK_ID --project PEER_PROJECT_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&network, "network", "", "target network id (the receiving project's network, required)")
			fs.StringVar(&project, "project", "", "attaching (peer) project id (required)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 0 {
				return usageErr(name, "takes no positional arguments")
			}
			if network == "" || project == "" {
				return usageErr(name, "--network and --project are required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Networks.DeclareNetworkPeer(ctx, &structurev1.DeclareNetworkPeerRequest{
				NetworkId: network, PeerProjectId: project,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetPeer(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "declared peer %s on network %s (state %s)\n",
					resp.GetPeer().GetId(), resp.GetPeer().GetNetworkName(), resp.GetPeer().GetState())
			})
		},
	}
}

func newNetworksApproveVerb() commands.Command {
	const name = "approve"
	return &flaggedVerb{
		name:     name,
		synopsis: "Approve a pending peer attachment (receiving project's side)",
		usage:    "networks approve PEER_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one PEER_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Networks.ApproveNetworkPeer(ctx, &structurev1.ApproveNetworkPeerRequest{Id: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetPeer(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "approved peer %s (state %s)\n",
					resp.GetPeer().GetId(), resp.GetPeer().GetState())
			})
		},
	}
}

func newNetworksRevokeVerb() commands.Command {
	const name = "revoke"
	return &flaggedVerb{
		name:     name,
		synopsis: "Revoke a peer attachment (either side; isolates existing workloads immediately)",
		usage:    "networks revoke PEER_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one PEER_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Networks.RevokeNetworkPeer(ctx, &structurev1.RevokeNetworkPeerRequest{Id: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetPeer(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "revoked peer %s (state %s)\n",
					resp.GetPeer().GetId(), resp.GetPeer().GetState())
			})
		},
	}
}

func newNetworksPeersVerb() commands.Command {
	const name = "peers"
	var network, project, after string
	var limit int
	return &flaggedVerb{
		name:     name,
		synopsis: "List cross-project peer declarations (filter by --network or --project)",
		usage:    "networks peers [--network NETWORK_ID] [--project PEER_PROJECT_ID] [--after PEER_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&network, "network", "", "filter by receiving network id")
			fs.StringVar(&project, "project", "", "filter by attaching (peer) project id")
			fs.StringVar(&after, "after", "", "pagination cursor: the last peer id of the previous page")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 0 {
				return usageErr(name, "takes no positional arguments")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Networks.ListNetworkPeers(ctx, &structurev1.ListNetworkPeersRequest{
				NetworkId: network, PeerProjectId: project, AfterPeerId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tSTATE\tNETWORK\tNETWORK_PROJECT\tPEER_PROJECT")
				for _, p := range resp.GetPeers() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\n",
						p.GetId(), p.GetState(), p.GetNetworkName(), p.GetNetworkProjectId(), p.GetPeerProjectId())
				}
			})
		},
	}
}
