package cmd

// 能力自描述动词（F1.4，架构 §7"porter 模式"）：schema 列全量契约清单、
// explain 展开单资源 JSON Schema——Agent 的零文档发现面（服务器单源
// SystemService.GetSchema/Explain，CLI 是瘦渲染）。自描述面 PUBLIC：无
// 凭证亦可发现契约（安装引导期可用）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/lynx-go/commands"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
)

// newSchemaCmd 列出自描述全量清单（Spec 契约 + 事件 payload schema）。
func newSchemaCmd() commands.Command {
	const name = "schema"
	return &flaggedVerb{
		name:     name,
		synopsis: "List the platform self-description (spec contracts and event payload schemas)",
		usage:    "schema",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.System.GetSchema(ctx, &systemv1.GetSchemaRequest{})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeProtoJSON(env.Stdout, resp)
			}
			_, err = fmt.Fprintln(env.Stdout, "NAME\tKIND\tSUMMARY")
			if err != nil {
				return err
			}
			for _, e := range resp.GetEntries() {
				if _, err := fmt.Fprintf(env.Stdout, "%s\t%s\t%s\n", e.GetName(), e.GetKind(), e.GetSummary()); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintln(env.Stdout, "run 'fleetly explain <name>' for the full JSON Schema")
			return err
		},
	}
}

// newExplainVerb 展开单个资源的自描述（Spec 种类或事件名）。
func newExplainVerb() commands.Command {
	const name = "explain"
	return &flaggedVerb{
		name:     name,
		synopsis: "Explain one resource: its JSON Schema and summary (spec kind or event name)",
		usage:    "explain RESOURCE (e.g. app, task, database, deployment.succeeded; run 'fleetly schema' to list)",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return &commands.UsageError{
					Usage: name,
					Err:   fmt.Errorf("exactly one resource name is required (run 'fleetly schema' to list)"),
				}
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.System.Explain(ctx, &systemv1.ExplainRequest{Resource: args[0]})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeProtoJSON(env.Stdout, resp)
			}
			e := resp.GetEntry()
			if _, err := fmt.Fprintf(env.Stdout, "%s (%s) — %s\n", e.GetName(), e.GetKind(), e.GetSummary()); err != nil {
				return err
			}
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, []byte(e.GetSchemaJson()), "", "  "); err != nil {
				return fmt.Errorf("render schema for %s: %w", e.GetName(), err)
			}
			_, err = fmt.Fprintln(env.Stdout, pretty.String())
			return err
		},
	}
}
