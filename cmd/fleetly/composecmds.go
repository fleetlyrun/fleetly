package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/envlayer"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// validateCmd 实现 fleetly validate <path>：校验 + 归一化。--json 输出
// 机器形态；退出码 0（有效）/ 1（校验失败）。
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
		raw, err := marshalIndentJSON(result)
		if err != nil {
			return err
		}
		_, err = env.Stdout.Write(raw)
		return err
	}
	var b strings.Builder
	writeValidateHuman(&b, result, warnings)
	_, err = fmt.Fprint(env.Stdout, b.String())
	return err
}

// planCmd 实现 fleetly plan <path>：校验 + 归一化 + 与基线 diff，输出
// plan artifact（spec_hash 即 etag）。三态退出码：0 无变化 / 2 有变化 /
// 1 错误。--baseline 指定另一 compose 文件作基线（真实 DB 基线随引擎票；
// 缺省 = 空基线，即首部署语义，一切服务视为新增）。--output 将 artifact
// 落盘（apply 前校验 etag 防 stale 并发的机制位）。--db 打开状态库读取
// 平台 env_vars，把 W_ENV_PLATFORM_OVERRIDE 覆盖告警并入计划（architecture
// §2.4 变量合并行；pending 条目计入——下次部署即其生效点）。apply 动词随
// 引擎层（T2.10）接入：本期不改运行态，不做假 apply。
type planCmd struct {
	jsonOut  bool
	baseline string
	output   string
	dbPath   string
}

func (c *planCmd) Name() string { return "plan" }
func (c *planCmd) Synopsis() string {
	return "plan changes of a compose file against a baseline (exit 2 = changes)"
}
func (c *planCmd) Usage() string {
	return "plan [--baseline <compose-file>] [--output <artifact-file>] [--db <state-db>] [--json] <compose-file>"
}

func (c *planCmd) SetFlags(fs *flag.FlagSet) {
	fs.BoolVar(&c.jsonOut, "json", false, "output the plan artifact as JSON")
	fs.StringVar(&c.baseline, "baseline", "", "baseline compose file (default: empty baseline, first-deploy semantics)")
	fs.StringVar(&c.output, "output", "", "write the plan artifact to a file")
	fs.StringVar(&c.dbPath, "db", "", "state db path for platform env override warnings (W_ENV_PLATFORM_OVERRIDE)")
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
	}
	plan, err := compose.Diff(base, target)
	if err != nil {
		return err
	}
	// 校验期警告（无 healthcheck、cron label v0.2 提示）随 artifact 带出。
	plan.Warnings = append(plan.Warnings, warnings...)
	// 平台 env_vars 覆盖告警（--db 提供状态库时；DB 缺省关闭，plan 保持
	// 纯 compose 语义）。
	platformWarnings, err := c.platformOverrideWarnings(ctx, target)
	if err != nil {
		return err
	}
	plan.Warnings = append(plan.Warnings, platformWarnings...)
	if err := c.emit(env, plan); err != nil {
		return err
	}
	if plan.HasChanges {
		return errChanges
	}
	return nil
}

// platformOverrideWarnings 读取状态库平台 env_vars（effective + pending）
// 并产出 W_ENV_PLATFORM_OVERRIDE 警告。--db 未给或库文件不存在 → 无警告
// （plan 的缺省形态不依赖状态库）。
func (c *planCmd) platformOverrideWarnings(ctx context.Context, target *compose.Spec) ([]compose.Warning, error) {
	if c.dbPath == "" {
		return nil, nil
	}
	if _, err := os.Stat(c.dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat state db %s: %w", c.dbPath, err)
	}
	st, err := state.Open(ctx, c.dbPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()
	app, err := st.GetAppByName(ctx, target.Name)
	if errors.Is(err, state.ErrAppNotFound) {
		return nil, nil // 应用未创建 → 平台层必空 → 无覆盖
	}
	if err != nil {
		return nil, err
	}
	rows, err := st.ListAppEnv(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	platform := make([]envlayer.PlatformVar, 0, len(rows))
	for _, r := range rows {
		source := r.Source
		if source == "" {
			source = "platform"
		}
		platform = append(platform, envlayer.PlatformVar{Key: r.Key, Source: source})
	}
	return envlayer.PlatformOverrideWarnings(target, platform), nil
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
		if err := os.WriteFile(c.output, artifact, 0o600); err != nil {
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

// diffCmd 实现 fleetly diff <a> <b>：两份 compose 归一化差异。退出码与
// plan 同三态。
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
	if err != nil {
		return err
	}
	if c.jsonOut {
		artifact, err := marshalIndentJSON(plan)
		if err != nil {
			return err
		}
		if _, err := env.Stdout.Write(artifact); err != nil {
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

// requireArgs 校验位置参数数量（usage 违规统一 UsageError→退出 2）。
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
