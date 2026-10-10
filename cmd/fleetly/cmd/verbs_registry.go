package cmd

// Registry 内容动词（IA v3 二期⑤b）：受管镜像仓只读代理——catalog 按
// 项目前纲过滤、tags 携带 digest/大小/推送时刻。全部 --json 双形态
//（golden 钉死）；仓名/镜像引用不含凭证值。

import (
	"context"
	"flag"
	"fmt"

	"github.com/lynx-go/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
)

func newRegistryCatalogVerb() commands.Command {
	const name = "catalog"
	var project string
	return &flaggedVerb{
		name:     name,
		synopsis: "List image repositories under the project prefix in the managed registry",
		usage:    "registry catalog --project PROJECT_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "owning project id (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
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
			resp, err := c.Registry.ListRegistryCatalog(ctx, &deliveryv1.ListRegistryCatalogRequest{ProjectId: project})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				for _, repo := range resp.GetRepositories() {
					_, _ = fmt.Fprintln(env.Stdout, repo)
				}
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
			fs.StringVar(&project, "project", "", "owning project id (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one REPOSITORY argument")
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
			resp, err := c.Registry.ListRegistryTags(ctx, &deliveryv1.ListRegistryTagsRequest{ProjectId: project, Repository: args[0]})
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
		},
	}
}
