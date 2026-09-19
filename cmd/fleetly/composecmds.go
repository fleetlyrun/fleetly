package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/lynx-go/commands"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/compose"
)

// validateCmd 实现 fleetly validate <path>：校验 + 归一化（纯本地解析——
// internal/compose 纯库，不经 daemon）。--json 输出机器形态；退出码
// 0（有效）/ 1（校验失败）。
type validateCmd struct {
	jsonOut bool
}

func (c *validateCmd) Name() string { return "validate" }
func (c *validateCmd) Synopsis() string {
	return "validate a compose file against the fleetly controlled subset"
}
func (c *validateCmd) Usage() string { return "validate [--json] <compose-file>" }

func (c *validateCmd) SetFlags(fs *flag.FlagSet) {
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *validateCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	spec, warnings, err := compose.Load(ctx, args[0])
	if err != nil {
		return err
	}
	result := validateResult{
		Valid:    true,
		Name:     spec.Name,
		SpecHash: spec.SpecHash,
		Services: len(spec.Services),
		Volumes:  len(spec.Volumes),
		Warnings: warnings,
	}
	if c.jsonOut {
		return writeJSON(env.Stdout, result)
	}
	var b strings.Builder
	writeValidateHuman(&b, result, warnings)
	_, err = fmt.Fprint(env.Stdout, b.String())
	return err
}

// planCmd 实现 fleetly plan <path>：校验 + 归一化 + 与基线 diff，输出
// plan artifact（spec_hash 即 etag）。三态退出码：0 无变化 / 2 有变化 /
// 1 错误。基线解析（T2.18 CLI-over-SDK 改造）：--baseline 给文件则本地
// 比；未给则经 RPC 取该 app 最近成功 revision 的归一化 compose 快照为
// 基线（ListRevisions + GetRevisionSpec），无任何 revision = 空基线（首
// 部署语义）。--output 将 artifact 落盘（apply 前校验 etag 防 stale 并发
// 的机制位）。平台 env 覆盖告警（W_ENV_PLATFORM_OVERRIDE）的合并输入是
// 平台 env 台账——经 RPC 读取随 env 面后续开放（v0.1 plan 保持纯 compose
// 语义，遗留记录）。
type planCmd struct {
	jsonOut  bool
	baseline string
	output   string
	conn     connFlags
}

func (c *planCmd) Name() string { return "plan" }
func (c *planCmd) Synopsis() string {
	return "plan changes of a compose file against a baseline (exit 2 = changes)"
}
func (c *planCmd) Usage() string {
	return "plan [--baseline <compose-file>] [--output <artifact-file>] [--addr <host:port>] [--token <tok>] [--json] <compose-file>"
}

func (c *planCmd) SetFlags(fs *flag.FlagSet) {
	fs.BoolVar(&c.jsonOut, "json", false, "output the plan artifact as JSON")
	fs.StringVar(&c.baseline, "baseline", "", "baseline compose file (default: latest succeeded revision via RPC; none = first-deploy semantics)")
	fs.StringVar(&c.output, "output", "", "write the plan artifact to a file")
	c.conn.register(fs)
}

func (c *planCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	target, warnings, err := compose.Load(ctx, args[0])
	if err != nil {
		return err
	}
	var base *compose.Spec
	if c.baseline != "" {
		if base, _, err = compose.Load(ctx, c.baseline); err != nil {
			return err
		}
	} else {
		// RPC 基线：最近成功 revision 的归一化 compose 快照（env 为
		// key:sha256——脱敏结构性成立）。连接失败/无 revision 的裁决在
		// fetchBaselineSpec。
		if base, err = c.fetchBaselineSpec(ctx, target.Name); err != nil {
			return err
		}
	}
	plan, err := compose.Diff(base, target)
	if err != nil {
		return err
	}
	// 校验期警告（无 healthcheck、cron label v0.2 提示）随 artifact 带出。
	plan.Warnings = append(plan.Warnings, warnings...)
	if err := c.emit(env, plan); err != nil {
		return err
	}
	if plan.HasChanges {
		return errChanges
	}
	return nil
}

// fetchBaselineSpec 经 RPC 取该 app 最近成功 revision 的归一化 compose
// 快照并还原为 Spec。应用不存在（尚未首部署）或无任何 revision = 空基线
// （首部署语义，非错误——plan 常先于首次部署执行）。快照的退化形态
// （compose 文件丢失时的哈希摘要）解析不出服务 → 空基线同语义。
func (c *planCmd) fetchBaselineSpec(ctx context.Context, appName string) (*compose.Spec, error) {
	var base *compose.Spec
	err := c.conn.withClient(func(cl *fleetlyClient) error {
		base = compose.LoadEmpty(appName) // 缺省：首部署语义
		revs, err := cl.Revisions().ListRevisions(ctx, &serverv1.ListRevisionsRequest{App: appName})
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return nil // 应用未创建 = 必无快照 → 首部署语义
			}
			return err
		}
		if len(revs.GetRevisions()) == 0 {
			return nil
		}
		latest := revs.GetRevisions()[0] // seq 降序，首项 = 最近成功
		specResp, err := cl.Revisions().GetRevisionSpec(ctx, &serverv1.GetRevisionSpecRequest{
			App: appName, RevisionId: latest.GetId(),
		})
		if err != nil {
			return err
		}
		spec := &compose.Spec{}
		if err := json.Unmarshal([]byte(specResp.GetCompose()), spec); err != nil {
			return nil // 退化快照（哈希摘要形态）不是 Spec JSON——空基线同语义
		}
		base = spec
		return nil
	})
	return base, err
}

// emit 输出 plan artifact：--output 落盘（人读摘要仍上 stdout）；--json
// 全文上 stdout；否则人读报告。
func (c *planCmd) emit(env *commands.Environment, plan *compose.Plan) error {
	artifact, err := marshalIndentJSON(plan)
	if err != nil {
		return err
	}
	switch {
	case c.output != "":
		if err := os.WriteFile(c.output, artifact, 0o600); err != nil { //nolint:gosec // artifact 为调用方 --output 指定的路径
			return fmt.Errorf("write artifact %s: %w", c.output, err)
		}
		if _, err := fmt.Fprintf(env.Stdout, "plan artifact written to %s\n", c.output); err != nil {
			return err
		}
		_, err = fmt.Fprint(env.Stdout, plan.RenderText())
		return err
	case c.jsonOut:
		_, err = env.Stdout.Write(artifact)
		return err
	default:
		_, err = fmt.Fprint(env.Stdout, plan.RenderText())
		return err
	}
}

// diffCmd 实现 fleetly diff <a> <b>：两份 compose 归一化差异（纯本地）。
// 退出码与 plan 同三态。
type diffCmd struct {
	jsonOut bool
}

func (c *diffCmd) Name() string { return "diff" }
func (c *diffCmd) Synopsis() string {
	return "diff two compose files by their normalized specs (exit 2 = changes)"
}
func (c *diffCmd) Usage() string { return "diff [--json] <compose-a> <compose-b>" }

func (c *diffCmd) SetFlags(fs *flag.FlagSet) {
	fs.BoolVar(&c.jsonOut, "json", false, "output the plan artifact as JSON")
}

func (c *diffCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	a, warnings, err := compose.Load(ctx, args[0])
	if err != nil {
		return err
	}
	b, _, err := compose.Load(ctx, args[1])
	if err != nil {
		return err
	}
	plan, err := compose.Diff(a, b)
	if err != nil {
		return err
	}
	plan.Warnings = append(plan.Warnings, warnings...)
	if c.jsonOut {
		if err := writeJSON(env.Stdout, plan); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprint(env.Stdout, plan.RenderText()); err != nil {
			return err
		}
	}
	if plan.HasChanges {
		return errChanges
	}
	return nil
}

// requireArgs 校验位置参数数量（usage 违规统一 UsageError→退出 64，S17-D3）。
func requireArgs(usage string, args []string, n int) error {
	if len(args) != n {
		return &commands.UsageError{Usage: usage, Err: fmt.Errorf("expected %d argument(s), got %d", n, len(args))}
	}
	return nil
}

// 编译期断言：动词实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &validateCmd{}
	_ commands.Flagged = &validateCmd{}
	_ commands.Command = &planCmd{}
	_ commands.Flagged = &planCmd{}
	_ commands.Command = &diffCmd{}
	_ commands.Flagged = &diffCmd{}
)
