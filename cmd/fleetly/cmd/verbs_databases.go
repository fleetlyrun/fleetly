package cmd

// Databases 上下文动词（F1.12，ADR-0029）。全部 --json 双形态（golden 钉
// 死）；凭证值永不回显——create 回显 Secret 名（database:<name>），App 经
// process secret_refs 引用后取完整连接 URL。

import (
	"context"
	"flag"
	"fmt"

	"github.com/lynx-go/commands"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
)

func newDatabasesCreateVerb() commands.Command {
	const name = "create"
	var project, engine string
	var idem idemKeyFlag
	return &flaggedVerb{
		name: name, synopsis: "Create a database from a template (postgres / pgvector / redis)",
		usage: "databases create --project PROJECT_ID --engine ENGINE NAME",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "owning project id (required)")
			fs.StringVar(&engine, "engine", "", "template engine: postgres | pgvector | redis (required)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if project == "" || engine == "" {
				return usageErr(name, "--project and --engine are required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Databases.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
				ProjectId: project, Name: args[0], Engine: engine,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetDatabase(), func() {
				_, _ = fmt.Fprintf(env.Stdout,
					"created database %s (id %s)\nengine %s %s\ncredential secret %s (value never shown; reference it from app process secret_refs)\nconnect in-network at %s:%d\n",
					resp.GetDatabase().GetName(), resp.GetDatabase().GetId(),
					resp.GetDatabase().GetEngine(), resp.GetDatabase().GetVersion(),
					resp.GetDatabase().GetCredentialsRef(),
					resp.GetDatabase().GetHost(), resp.GetDatabase().GetPort())
			})
		},
	}
}

func newDatabasesListVerb() commands.Command {
	const name = "list"
	var project, after string
	var limit int
	return &flaggedVerb{
		name: name, synopsis: "List databases in a project (newest first)",
		usage: "databases list --project PROJECT_ID [--after DATABASE_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.StringVar(&after, "after", "", "pagination cursor: the last database id of the previous page")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
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
			resp, err := c.Databases.ListDatabases(ctx, &structurev1.ListDatabasesRequest{
				ProjectId: project, AfterDatabaseId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tNAME\tENGINE\tVERSION\tSTATUS\tCREDENTIALS\tCREATED")
				for _, d := range resp.GetDatabases() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						d.GetId(), d.GetName(), d.GetEngine(), d.GetVersion(), d.GetStatus(), d.GetCredentialsRef(), d.GetCreatedAt())
				}
			})
		},
	}
}

func newDatabasesGetVerb() commands.Command {
	const name = "get"
	return &flaggedVerb{
		name: name, synopsis: "Show one database (connection face without credentials)",
		usage: "databases get DATABASE_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Databases.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetDatabase(), func() {
				d := resp.GetDatabase()
				_, _ = fmt.Fprintf(env.Stdout,
					"id %s\nname %s\nengine %s %s\nstatus %s\ncredential secret %s (value never shown)\nin-network endpoint %s:%d\ncreated %s\n",
					d.GetId(), d.GetName(), d.GetEngine(), d.GetVersion(), d.GetStatus(),
					d.GetCredentialsRef(), d.GetHost(), d.GetPort(), d.GetCreatedAt())
			})
		},
	}
}

// databases delete（ADR-0029 决策 8）：收口删除——拆载体 + tombstone；
// 数据卷与凭证 Secret 残留（Project 级材料，备份保留义）。
func newDatabasesDeleteVerb() commands.Command {
	const name = "delete"
	return &flaggedVerb{
		name: name, synopsis: "Delete a database (tears down carriers; volume and credential secret are retained)",
		usage: "databases delete DATABASE_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Databases.DeleteDatabase(ctx, &structurev1.DeleteDatabaseRequest{Id: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintf(env.Stdout,
					"deleted database %s (carriers torn down; data volume and credential secret retained)\n", args[0])
			})
		},
	}
}
