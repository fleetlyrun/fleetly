package main

// 构建命令（T2.8/T2.9）：`fleetly build <compose>` 入队并等待完成（轮询
// builds 状态）→ 输出不可变 digest；`fleetly builds list <app>` 查构建
// 历史。与 env/placement/nodes 同形态：CLI 直连本地状态库（gRPC API 面
// 随 T2.17 统一接线）。
//
// 执行拓扑：入队（builds queued 行）与执行（fleetlyd build.queue 服务扫描
// 认领）跨进程解耦——daemon 不在运行时构建停留 queued（--timeout 超时给
// 出可行动提示）。镜像模式服务（仅 image 无 build）= 无构建直通：不建行、
// 报告引用（部署以 digest 引用随引擎票接入）。

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lynx-go/commands"
	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/build"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// buildPollInterval 是 CLI 轮询 builds 行的周期。
const buildPollInterval = time.Second

// defaultBuildTimeout 是 build 等待缺省上限（冷构建受外网主导、方差极大
// ——Spike A 实测 85s~900s 量级；45 分钟覆盖慢网首建）。
const defaultBuildTimeout = 45 * time.Minute

// buildCmd 实现 `fleetly build <compose-file>`。
type buildCmd struct {
	service string
	jsonOut bool
	dbPath  string
	timeout time.Duration
}

func (c *buildCmd) Name() string { return "build" }
func (c *buildCmd) Synopsis() string {
	return "build services of a compose file (queued into fleetlyd, waits and prints digests)"
}
func (c *buildCmd) Usage() string {
	return "build [--service <name>] [--db <state-db>] [--timeout <duration>] [--json] <compose-file>"
}

func (c *buildCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.service, "service", "", "build only this service (default: all build-mode services)")
	fs.StringVar(&c.dbPath, "db", defaultDBPath, "state db path (shared with fleetlyd)")
	fs.DurationVar(&c.timeout, "timeout", defaultBuildTimeout, "wait limit for builds to finish")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *buildCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	spec, warnings, err := compose.Load(ctx, args[0])
	if err != nil {
		return err
	}
	composeDir := filepath.Dir(args[0])

	st, cleanup, err := openStore(c.dbPath)
	if err != nil {
		return err
	}
	defer func() { cleanup() }()

	app, err := c.ensureApp(ctx, st, spec.Name)
	if err != nil {
		return err
	}

	targets, passthrough, err := selectBuildTargets(spec, composeDir, c.service)
	if err != nil {
		return err
	}

	// 入队：构建ID 在入队侧分配（Request 与 builds 行共用同一 ID，一次
	// 建行原子落库——无两步写）。
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		id := ulid.Make().String()
		request := build.Request{
			BuildID:    id,
			AppID:      app.ID,
			AppName:    spec.Name,
			Service:    t.Service,
			Driver:     t.Driver,
			ContextDir: t.ContextDir,
			Dockerfile: t.Dockerfile,
			SpecHash:   spec.SpecHash,
		}
		raw, err := request.Encode()
		if err != nil {
			return err
		}
		if _, err := st.CreateBuild(ctx, state.BuildRecord{
			ID:      id,
			AppID:   app.ID,
			Service: t.Service,
			Driver:  t.Driver,
			Request: raw,
		}); err != nil {
			return err
		}
		ids = append(ids, id)
	}

	// 等待全部到终态（daemon 不在运行 → queued 超时，给可行动提示）。
	results, err := c.wait(ctx, env, st, ids, c.timeout)
	if err != nil {
		return err
	}

	if err := c.emit(env, spec.Name, results, passthrough, warnings); err != nil {
		return err
	}
	for _, r := range results {
		if r.Status != state.BuildSucceeded {
			if r.ErrorCode != "" {
				return fmt.Errorf("build %s (%s) failed: %s（构建日志 %s）", r.ID, r.Service, r.ErrorCode, r.LogPath)
			}
			return fmt.Errorf("build %s (%s) ended as %s", r.ID, r.Service, r.Status)
		}
	}
	return nil
}

// ensureApp 取应用行；不存在则创建（构建常是应用的第一个平台动作；
// env/placement 命令只操作既有应用的语义不受影响）。
func (c *buildCmd) ensureApp(ctx context.Context, st *state.Store, name string) (state.App, error) {
	app, err := st.GetAppByName(ctx, name)
	if err == nil {
		return app, nil
	}
	if !errors.Is(err, state.ErrAppNotFound) {
		return state.App{}, err
	}
	created, err := st.CreateApp(ctx, "", name)
	if err != nil {
		return state.App{}, fmt.Errorf("create app %s: %w", name, err)
	}
	return created, nil
}

// buildTarget 是一次待入队构建的解析结果。
type buildTarget struct {
	Service    string
	Driver     state.Driver
	ContextDir string
	Dockerfile string
}

// passthroughService 是镜像模式服务的直通报告。
type passthroughService struct {
	Service string
	Image   string
}

// selectBuildTargets 按服务声明裁决构建目标（--service 过滤）。
func selectBuildTargets(spec *compose.Spec, composeDir, only string) ([]buildTarget, []passthroughService, error) {
	var targets []buildTarget
	var passthrough []passthroughService
	for _, svc := range spec.Services {
		if only != "" && svc.Name != only {
			continue
		}
		driver, buildable, err := build.DriverFor(svc)
		if err != nil {
			return nil, nil, err
		}
		if !buildable {
			passthrough = append(passthrough, passthroughService{Service: svc.Name, Image: svc.Image})
			continue
		}
		contextDir, err := filepath.Abs(filepath.Join(composeDir, svc.Build.Context))
		if err != nil {
			return nil, nil, fmt.Errorf("resolve build context of service %s: %w", svc.Name, err)
		}
		targets = append(targets, buildTarget{
			Service:    svc.Name,
			Driver:     driver,
			ContextDir: contextDir,
			Dockerfile: svc.Build.Dockerfile,
		})
	}
	if only != "" && len(targets) == 0 && len(passthrough) == 0 {
		return nil, nil, fmt.Errorf("service %q not found in compose %q", only, spec.Name)
	}
	return targets, passthrough, nil
}

// wait 轮询至全部构建到终态或超时。超时区分「仍 queued」（daemon 未运行）
// 与「执行中超时」（卡住的构建），给出不同提示。
func (c *buildCmd) wait(ctx context.Context, env *commands.Environment, st *state.Store, ids []string, timeout time.Duration) ([]state.BuildRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	pending := make(map[string]bool, len(ids))
	for _, id := range ids {
		pending[id] = true
	}
	out := make([]state.BuildRecord, 0, len(ids))
	for len(pending) > 0 {
		select {
		case <-deadline.C:
			queued := c.countStatus(ctx, st, ids, state.BuildQueued)
			if queued == len(ids) {
				return out, fmt.Errorf("builds stayed queued for %s——fleetlyd 未运行？构建由 fleetlyd 的 build.queue 服务执行（本命令只入队与等待）", timeout)
			}
			return out, fmt.Errorf("builds did not finish within %s（在途构建继续执行，fleetly builds list 可查）", timeout)
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(buildPollInterval):
		}
		for id := range pending {
			rec, err := st.GetBuild(ctx, id)
			if err != nil {
				return out, err
			}
			if rec.Status == state.BuildQueued || rec.Status == state.BuildBuilding {
				continue
			}
			delete(pending, id)
			out = append(out, rec)
			if !c.jsonOut {
				line := fmt.Sprintf("build %s (%s) %s", rec.ID, rec.Service, rec.Status)
				if rec.Status == state.BuildSucceeded {
					line += "  " + rec.ImageRef + "@" + rec.ImageDigest
				}
				if _, err := fmt.Fprintln(env.Stdout, line); err != nil {
					return out, err
				}
			}
		}
	}
	return out, nil
}

// countStatus 统计处于某状态的构建数（超时提示的归因）。
func (c *buildCmd) countStatus(ctx context.Context, st *state.Store, ids []string, want state.BuildStatus) int {
	n := 0
	for _, id := range ids {
		rec, err := st.GetBuild(ctx, id)
		if err == nil && rec.Status == want {
			n++
		}
	}
	return n
}

// buildResultJSON 是 build --json 输出形态。
type buildResultJSON struct {
	App         string               `json:"app"`
	Builds      []buildRecordJSON    `json:"builds"`
	Passthrough []passthroughService `json:"passthrough,omitempty"`
	Warnings    []compose.Warning    `json:"warnings,omitempty"`
}

// buildRecordJSON 是构建行的机器形态（时间 RFC3339、枚举原样）。
type buildRecordJSON struct {
	ID          string `json:"id"`
	Service     string `json:"service"`
	Driver      string `json:"driver"`
	Status      string `json:"status"`
	ImageRef    string `json:"image_ref,omitempty"`
	ImageDigest string `json:"image_digest,omitempty"`
	LogPath     string `json:"log_path,omitempty"`
	PlanPath    string `json:"plan_path,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
}

func toBuildRecordJSON(rec state.BuildRecord) buildRecordJSON {
	out := buildRecordJSON{
		ID:          rec.ID,
		Service:     rec.Service,
		Driver:      string(rec.Driver),
		Status:      string(rec.Status),
		ImageRef:    rec.ImageRef,
		ImageDigest: rec.ImageDigest,
		LogPath:     rec.LogPath,
		PlanPath:    rec.PlanPath,
		ErrorCode:   rec.ErrorCode,
	}
	if !rec.StartedAt.IsZero() {
		out.StartedAt = rec.StartedAt.Format(time.RFC3339)
	}
	if !rec.FinishedAt.IsZero() {
		out.FinishedAt = rec.FinishedAt.Format(time.RFC3339)
	}
	return out
}

// emit 输出构建结果（--json 机器形态 / 人读摘要）。
func (c *buildCmd) emit(env *commands.Environment, appName string, results []state.BuildRecord, passthrough []passthroughService, warnings []compose.Warning) error {
	if c.jsonOut {
		out := buildResultJSON{
			App:         appName,
			Passthrough: passthrough,
			Warnings:    warnings,
		}
		for _, rec := range results {
			out.Builds = append(out.Builds, toBuildRecordJSON(rec))
		}
		raw, err := marshalIndentJSON(out)
		if err != nil {
			return err
		}
		_, err = env.Stdout.Write(raw)
		return err
	}
	for _, p := range passthrough {
		if _, err := fmt.Fprintf(env.Stdout, "service %s: image mode（无构建直通）%s\n", p.Service, p.Image); err != nil {
			return err
		}
	}
	var b strings.Builder
	writeWarnings(&b, warnings)
	_, err := fmt.Fprint(env.Stdout, b.String())
	return err
}

// ── fleetly builds ──────────────────────────────────────────────────────────

// buildsCmd 是外层动词 `builds`：分发 list。
type buildsCmd struct {
	sub *commands.App
}

func newBuildsCmd() *buildsCmd {
	sub := commands.New()
	sub.Register(&buildsListCmd{})
	sub.VerbTitle = "builds subcommands:"
	return &buildsCmd{sub: sub}
}

func (c *buildsCmd) Name() string { return "builds" }
func (c *buildsCmd) Synopsis() string {
	return "build history (state: queued/building/succeeded/failed)"
}
func (c *buildsCmd) Usage() string { return "builds list [--db <state-db>] [--json] <app>" }

func (c *buildsCmd) SetFlags(_ *flag.FlagSet) {}

func (c *buildsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (list)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// buildsListCmd 实现 `fleetly builds list <app>`。
type buildsListCmd struct {
	jsonOut bool
	dbPath  string
	limit   int
}

func (c *buildsListCmd) Name() string { return "list" }
func (c *buildsListCmd) Synopsis() string {
	return "list builds of an app (newest first, digest per succeeded build)"
}
func (c *buildsListCmd) Usage() string {
	return "builds list [--db <state-db>] [--limit N] [--json] <app>"
}

func (c *buildsListCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.dbPath, "db", defaultDBPath, "state db path (shared with fleetlyd)")
	fs.IntVar(&c.limit, "limit", 20, "max rows")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *buildsListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	st, cleanup, err := openStore(c.dbPath)
	if err != nil {
		return err
	}
	defer func() { cleanup() }()

	app, err := lookupApp(ctx, st, args[0])
	if err != nil {
		return err
	}
	rows, err := st.ListAppBuilds(ctx, app.ID, c.limit)
	if err != nil {
		return err
	}
	if c.jsonOut {
		out := make([]buildRecordJSON, 0, len(rows))
		for _, rec := range rows {
			out = append(out, toBuildRecordJSON(rec))
		}
		return writeJSON(env, out)
	}
	if len(rows) == 0 {
		_, err = fmt.Fprintf(env.Stdout, "%s: no builds（fleetly build <compose> 发起构建）\n", app.Name)
		return err
	}
	for _, rec := range rows {
		line := fmt.Sprintf("%s  %-9s %-10s %-10s", rec.ID, rec.Status, rec.Driver, rec.Service)
		if rec.ImageDigest != "" {
			line += "  " + rec.ImageDigest
		}
		if rec.ErrorCode != "" {
			line += "  error=" + rec.ErrorCode
		}
		if _, err := fmt.Fprintln(env.Stdout, line); err != nil {
			return err
		}
	}
	return nil
}

// 编译期断言：构建命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &buildCmd{}
	_ commands.Flagged = &buildCmd{}
	_ commands.Command = &buildsCmd{}
	_ commands.Flagged = &buildsCmd{}
	_ commands.Command = &buildsListCmd{}
	_ commands.Flagged = &buildsListCmd{}
)
