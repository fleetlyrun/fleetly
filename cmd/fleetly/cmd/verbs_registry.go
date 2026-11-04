package cmd

// Registry 内容动词（IA v3 二期⑤b）：受管镜像仓只读代理——catalog 按
// 项目前纲过滤、tags 携带 digest/大小/推送时刻。全部 --json 双形态
//（golden 钉死）；仓名/镜像引用不含凭证值。

import (
	"context"
	"flag"
	"fmt"

	"github.com/lynxland/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func newRegistryCatalogVerb() commands.Command {
	const name = "catalog"
	var project string
	return &flaggedVerb{
		name:     name,
		synopsis: "List image repositories under the project prefix in the managed registry",
		usage:    "registry catalog --project PROJECT_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "owning project id or name (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if project == "" {
				return usageErr(name, "--project is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				projectID, err := resolveProjectID(ctx, c, project)
				if err != nil {
					return err
				}
				resp, err := c.Registry.ListRegistryCatalog(ctx, &deliveryv1.ListRegistryCatalogRequest{ProjectId: projectID})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					for _, repo := range resp.GetRepositories() {
						_, _ = fmt.Fprintln(env.Stdout, repo)
					}
				})
			})
		},
	}
}

func newRegistryTagsVerb() commands.Command {
	const name = "tags"
	var project string
	return &flaggedVerb{
		name:     name,
		synopsis: "List tags of one repository with digest, compressed size and push time",
		usage:    "registry tags --project PROJECT_ID REPOSITORY",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "owning project id or name (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one REPOSITORY argument")
			}
			if project == "" {
				return usageErr(name, "--project is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				projectID, err := resolveProjectID(ctx, c, project)
				if err != nil {
					return err
				}
				resp, err := c.Registry.ListRegistryTags(ctx, &deliveryv1.ListRegistryTagsRequest{ProjectId: projectID, Repository: args[0]})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintln(env.Stdout, "TAG\tDIGEST\tSIZE\tPUSHED")
					for _, tag := range resp.GetTags() {
						_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%d\t%s\n",
							tag.GetName(), tag.GetDigest(), tag.GetSizeBytes(), tag.GetPushedAt())
					}
				})
			})
		},
	}
}
