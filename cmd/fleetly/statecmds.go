package main

// 状态库直连命令（T2-3）：env / placement / nodes 三组只依赖本地 SQLite
// 与主密钥文件的运维命令。gRPC API 面随 T2.17 统一接线，本期 CLI 打开
// 状态库直读直写（与 fleetlyd 同一 state 层语义）；nodes 命令是观测
// 缓存的只读列表（state-model §2.2：禁止用于决策，展示/诊断专用）。
//
// env 生效语义（architecture §2.4 变量合并行）：set 创建 pending 变更、
// 随下次部署生效（发布引擎 T2.10 消费）；list 输出键名与状态位、值恒
// 脱敏，读值走 env get 显式路径。
//
// 动词形态（lynx-go/commands 嵌套子命令）：env set/get/list/rm、
// placement show、nodes ls——外层动词经内层 App 的 SubDispatch 复用
// 同一分发机器（README「嵌套子动词」模式）。

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/internal/placement"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// dbDefaults 是状态库直连命令的公共缺省（与 fleetlyd config 缺省一致）。
const (
	defaultDBPath  = "./fleetly.db"
	defaultKeyPath = "./fleetly.key"
)

// openStore 打开状态库（迁移幂等应用）；资源随命令进程退出释放。
func openStore(path string) (*state.Store, func(), error) {
	st, err := state.Open(context.Background(), path)
	if err != nil {
		return nil, nil, fmt.Errorf("open state db %s: %w", path, err)
	}
	return st, func() { _ = st.Close() }, nil
}

// lookupApp 按名取应用并统一错误文案。
func lookupApp(ctx context.Context, st *state.Store, name string) (state.App, error) {
	app, err := st.GetAppByName(ctx, name)
	if errors.Is(err, state.ErrAppNotFound) {
		return state.App{}, fmt.Errorf("app %q not found（应用随首次部署创建；env/placement 命令操作既有应用）", name)
	}
	return app, err
}

// loadBox 显式加载主密钥；allowCreate 允许首次生成（与 fleetlyd 首启
// 行为一致：生成即提示妥善保存——密钥与备份分离保存，丢失 = 全部 env
// 密文不可解）。只读路径（get）不允许隐式生成。
func loadBox(path string, allowCreate bool) (*secrets.Box, bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && !allowCreate {
		return nil, false, fmt.Errorf("master key %s 不存在：先启动 fleetlyd（首启生成），写路径可显式传 --create-key", path)
	}
	box, created, err := secrets.EnsureKey(path)
	if err != nil {
		return nil, false, err
	}
	return box, created, nil
}

// writeJSON 是 --json 输出统一编码（marshalIndentJSON + 换行）。
func writeJSON(env *commands.Environment, v any) error {
	raw, err := marshalIndentJSON(v)
	if err != nil {
		return err
	}
	_, err = env.Stdout.Write(raw)
	return err
}

// ── fleetly env ─────────────────────────────────────────────────────────────

// envCmd 是外层动词 `env`：分发 set/get/list/rm。
type envCmd struct {
	sub *commands.App
}

func newEnvCmd() *envCmd {
	sub := commands.New()
	sub.Register(&envSetCmd{}, &envGetCmd{}, &envListCmd{}, &envRmCmd{})
	sub.VerbTitle = "env subcommands:"
	return &envCmd{sub: sub}
}

func (c *envCmd) Name() string     { return "env" }
func (c *envCmd) Synopsis() string { return "platform env vars (three-layer merge: platform layer)" }
func (c *envCmd) Usage() string    { return "env <set|get|list|rm> [flags] <app> ..." }

func (c *envCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (set|get|list|rm)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// envSetCmd 实现 `fleetly env set <app> <key> <value>`：明文经 age envelope
// 加密入库（value 列只存密文），状态位 = pending（随下次部署生效）。
type envSetCmd struct {
	db        string
	key       string
	createKey bool
	jsonOut   bool
}

func (c *envSetCmd) Name() string { return "set" }
func (c *envSetCmd) Synopsis() string {
	return "set a platform env var (pending until next deploy)"
}
func (c *envSetCmd) Usage() string {
	return "env set [--db <path>] [--key <path>] [--create-key] [--json] <app> <key> <value>"
}

func (c *envSetCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.db, "db", defaultDBPath, "state db path")
	fs.StringVar(&c.key, "key", defaultKeyPath, "master key file path")
	fs.BoolVar(&c.createKey, "create-key", false, "create the master key on first use")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *envSetCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 3); err != nil {
		return err
	}
	appName, key, value := args[0], args[1], args[2]
	st, done, err := openStore(c.db)
	if err != nil {
		return err
	}
	defer done()
	app, err := lookupApp(ctx, st, appName)
	if err != nil {
		return err
	}
	box, created, err := loadBox(c.key, c.createKey)
	if err != nil {
		return err
	}
	if created {
		if _, err := fmt.Fprintf(env.Stdout, "master key generated at %s —妥善保存（与备份分离；丢失后 env 密文不可解）\n", box.Path()); err != nil {
			return err
		}
	}
	ciphertext, err := box.Encrypt([]byte(value))
	if err != nil {
		return err
	}
	if _, err := st.SetAppEnv(ctx, app.ID, key, string(ciphertext), "platform"); err != nil {
		return err
	}
	if c.jsonOut {
		return writeJSON(env, map[string]any{
			"app": appName, "key": key, "status": "pending", "effective_on": "next deploy",
		})
	}
	_, err = fmt.Fprintf(env.Stdout, "%s %s = *** (pending, effective on next deploy)\n", appName, key)
	return err
}

// envGetCmd 实现 `fleetly env get <app> <key>`：显式读回明文（操作者主动
// 取值路径；列表输出不落值）。
type envGetCmd struct {
	db      string
	key     string
	jsonOut bool
}

func (c *envGetCmd) Name() string { return "get" }
func (c *envGetCmd) Synopsis() string {
	return "read a platform env var (plaintext; explicit operator path)"
}
func (c *envGetCmd) Usage() string {
	return "env get [--db <path>] [--key <path>] [--json] <app> <key>"
}

func (c *envGetCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.db, "db", defaultDBPath, "state db path")
	fs.StringVar(&c.key, "key", defaultKeyPath, "master key file path")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *envGetCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	st, done, err := openStore(c.db)
	if err != nil {
		return err
	}
	defer done()
	box, _, err := loadBox(c.key, false)
	if err != nil {
		return err
	}
	app, err := lookupApp(ctx, st, args[0])
	if err != nil {
		return err
	}
	row, err := st.GetAppEnv(ctx, app.ID, args[1])
	if errors.Is(err, state.ErrEnvNotFound) {
		return fmt.Errorf("env %q not found on app %q", args[1], args[0])
	}
	if err != nil {
		return err
	}
	plaintext, err := box.Decrypt([]byte(row.Value))
	if err != nil {
		return err
	}
	if c.jsonOut {
		return writeJSON(env, map[string]any{
			"app": args[0], "key": row.Key, "value": string(plaintext), "status": string(row.Status),
		})
	}
	_, err = fmt.Fprintf(env.Stdout, "%s\n", plaintext)
	return err
}

// envListCmd 实现 `fleetly env list <app>`：键名 + 来源 + 状态位；值恒
// 脱敏（读值走 env get 显式路径）。
type envListCmd struct {
	db      string
	jsonOut bool
}

func (c *envListCmd) Name() string { return "list" }
func (c *envListCmd) Synopsis() string {
	return "list platform env vars (keys only; values masked)"
}
func (c *envListCmd) Usage() string {
	return "env list [--db <path>] [--json] <app>"
}

func (c *envListCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.db, "db", defaultDBPath, "state db path")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *envListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	st, done, err := openStore(c.db)
	if err != nil {
		return err
	}
	defer done()
	app, err := lookupApp(ctx, st, args[0])
	if err != nil {
		return err
	}
	rows, err := st.ListAppEnv(ctx, app.ID)
	if err != nil {
		return err
	}
	type item struct {
		Key    string `json:"key"`
		Source string `json:"source"`
		Status string `json:"status"`
		Value  string `json:"value"`
	}
	items := make([]item, 0, len(rows))
	for _, r := range rows {
		items = append(items, item{Key: r.Key, Source: r.Source, Status: string(r.Status), Value: "********"})
	}
	if c.jsonOut {
		return writeJSON(env, map[string]any{"app": args[0], "vars": items})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "app %s: %d platform env vars\n", args[0], len(items))
	for _, it := range items {
		fmt.Fprintf(&b, "  %s = ******** (%s, %s)\n", it.Key, it.Source, it.Status)
	}
	_, err = fmt.Fprint(env.Stdout, b.String())
	return err
}

// envRmCmd 实现 `fleetly env rm <app> <key>`（生效语义：当前运行 env 不变，
// 下次部署不再注入——「省略 = 删除」的期望态治理规则）。
type envRmCmd struct {
	db      string
	jsonOut bool
}

func (c *envRmCmd) Name() string { return "rm" }
func (c *envRmCmd) Synopsis() string {
	return "remove a platform env var (takes effect on next deploy)"
}
func (c *envRmCmd) Usage() string {
	return "env rm [--db <path>] [--json] <app> <key>"
}

func (c *envRmCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.db, "db", defaultDBPath, "state db path")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *envRmCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	st, done, err := openStore(c.db)
	if err != nil {
		return err
	}
	defer done()
	app, err := lookupApp(ctx, st, args[0])
	if err != nil {
		return err
	}
	if err := st.DeleteAppEnv(ctx, app.ID, args[1]); err != nil {
		if errors.Is(err, state.ErrEnvNotFound) {
			return fmt.Errorf("env %q not found on app %q", args[1], args[0])
		}
		return err
	}
	if c.jsonOut {
		return writeJSON(env, map[string]any{
			"app": args[0], "key": args[1], "removed": true, "effective_on": "next deploy",
		})
	}
	_, err = fmt.Fprintf(env.Stdout, "%s %s removed (current running env unchanged; next deploy no longer injects it)\n", args[0], args[1])
	return err
}

// ── fleetly placement ───────────────────────────────────────────────────────

// placementCmd 是外层动词 `placement`：分发 show（rebind 属 v0.2，不加）。
type placementCmd struct {
	sub *commands.App
}

func newPlacementCmd() *placementCmd {
	sub := commands.New()
	sub.Register(&placementShowCmd{})
	sub.VerbTitle = "placement subcommands:"
	return &placementCmd{sub: sub}
}

func (c *placementCmd) Name() string     { return "placement" }
func (c *placementCmd) Synopsis() string { return "app placement bindings (single-node view)" }
func (c *placementCmd) Usage() string    { return "placement <show> [flags] <app>" }

func (c *placementCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (show)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// placementShowCmd 实现 `fleetly placement show <app>`：绑定记录 + 卷注册
// 表 + 有卷约束字符串（运行位置卡片的数据面，stateful-placement §2.3）。
type placementShowCmd struct {
	db      string
	jsonOut bool
}

func (c *placementShowCmd) Name() string { return "show" }
func (c *placementShowCmd) Synopsis() string {
	return "show app placement binding, volumes and constraint"
}
func (c *placementShowCmd) Usage() string {
	return "placement show [--db <path>] [--json] <app>"
}

func (c *placementShowCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.db, "db", defaultDBPath, "state db path")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *placementShowCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	st, done, err := openStore(c.db)
	if err != nil {
		return err
	}
	defer done()
	app, err := lookupApp(ctx, st, args[0])
	if err != nil {
		return err
	}

	// 无绑定（ErrPlacementNotFound）是合法展示态——无卷应用自由调度；
	// 其余查询错误照常上抛。
	p, perr := st.GetPlacement(ctx, app.ID)
	var hasPlacement bool
	switch {
	case perr == nil:
		hasPlacement = true
	case errors.Is(perr, state.ErrPlacementNotFound):
		hasPlacement = false
	default:
		return perr
	}
	volumes, err := st.ListAppVolumes(ctx, app.ID)
	if err != nil {
		return err
	}

	type volumeView struct {
		Key       string `json:"key"`
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		NodeID    string `json:"node_id,omitempty"`
		MountPath string `json:"mount_path,omitempty"`
		Status    string `json:"status"`
	}
	out := struct {
		App        string       `json:"app"`
		Bound      bool         `json:"bound"`
		NodeID     string       `json:"node_id,omitempty"`
		State      string       `json:"state,omitempty"`
		Source     string       `json:"source,omitempty"`
		LabelRef   string       `json:"label_ref,omitempty"`
		Reason     string       `json:"reason,omitempty"`
		PinnedAt   string       `json:"pinned_at,omitempty"`
		Constraint string       `json:"constraint,omitempty"`
		Volumes    []volumeView `json:"volumes"`
	}{App: args[0], Volumes: []volumeView{}}
	if hasPlacement {
		out.Bound = p.State == state.PlacementBound
		out.NodeID = p.PlatformNodeID
		out.State = string(p.State)
		out.Source = string(p.Source)
		out.LabelRef = p.LabelRef
		out.Reason = p.Reason
		if !p.PinnedAt.IsZero() {
			out.PinnedAt = p.PinnedAt.Format("2006-01-02T15:04:05Z07:00")
		}
	}
	// 有卷 + 已绑定 → 约束编译结果（执行层随部署下发，placement.ConstraintFor）。
	if len(volumes) > 0 && out.NodeID != "" {
		out.Constraint = placement.ConstraintFor(out.NodeID)
	}
	for _, v := range volumes {
		out.Volumes = append(out.Volumes, volumeView{
			Key: v.Key, Name: v.Name, Kind: string(v.Kind),
			NodeID: v.PlatformNodeID, MountPath: v.MountPath, Status: string(v.Status),
		})
	}

	if c.jsonOut {
		return writeJSON(env, out)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "app %s\n", args[0])
	if hasPlacement {
		fmt.Fprintf(&b, "  placement: node %s (state=%s source=%s", out.NodeID, out.State, out.Source)
		if out.LabelRef != "" {
			fmt.Fprintf(&b, " label=%s", out.LabelRef)
		}
		if out.PinnedAt != "" {
			fmt.Fprintf(&b, " pinned_at=%s", out.PinnedAt)
		}
		b.WriteString(")\n")
	} else {
		b.WriteString("  placement: unbound (stateless apps are free to schedule)\n")
	}
	if out.Constraint != "" {
		fmt.Fprintf(&b, "  constraint: %s\n", out.Constraint)
	}
	if len(out.Volumes) > 0 {
		b.WriteString("  volumes:\n")
		for _, v := range out.Volumes {
			fmt.Fprintf(&b, "    %s %s (%s, %s", v.Key, v.Name, v.Kind, v.Status)
			if v.NodeID != "" {
				fmt.Fprintf(&b, " @%s", v.NodeID)
			}
			if v.MountPath != "" {
				fmt.Fprintf(&b, " -> %s", v.MountPath)
			}
			b.WriteString(")\n")
		}
	}
	_, err = fmt.Fprint(env.Stdout, b.String())
	return err
}

// ── fleetly nodes ───────────────────────────────────────────────────────────

// nodesCmd 是外层动词 `nodes`：分发 ls（只读观测面）。
type nodesCmd struct {
	sub *commands.App
}

func newNodesCmd() *nodesCmd {
	sub := commands.New()
	sub.Register(&nodesLsCmd{})
	sub.VerbTitle = "nodes subcommands:"
	return &nodesCmd{sub: sub}
}

func (c *nodesCmd) Name() string     { return "nodes" }
func (c *nodesCmd) Synopsis() string { return "cluster nodes (read-only observation cache)" }
func (c *nodesCmd) Usage() string    { return "nodes <ls> [flags]" }

func (c *nodesCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (ls)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// nodesLsCmd 实现 `fleetly nodes ls`：观测缓存只读列表（展示/诊断专用，
// state-model §2.2 禁止用于决策；节点变更用 docker node 原生命令）。
type nodesLsCmd struct {
	db      string
	jsonOut bool
}

func (c *nodesLsCmd) Name() string { return "ls" }
func (c *nodesLsCmd) Synopsis() string {
	return "list cluster nodes (observation cache; not for decisions)"
}
func (c *nodesLsCmd) Usage() string {
	return "nodes ls [--db <path>] [--json]"
}

func (c *nodesLsCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.db, "db", defaultDBPath, "state db path")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *nodesLsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	st, done, err := openStore(c.db)
	if err != nil {
		return err
	}
	defer done()
	nodes, err := st.ListCachedNodes(ctx)
	if err != nil {
		return err
	}
	type nodeView struct {
		SwarmNodeID  string            `json:"swarm_node_id"`
		PlatformID   string            `json:"platform_id,omitempty"`
		Hostname     string            `json:"hostname"`
		State        string            `json:"state"`
		Availability string            `json:"availability"`
		IsManager    bool              `json:"is_manager"`
		ObservedAt   string            `json:"observed_at"`
		Stale        bool              `json:"stale"`
		Labels       map[string]string `json:"labels,omitempty"`
	}
	views := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		v := nodeView{
			SwarmNodeID:  n.SwarmNodeID,
			Hostname:     n.Hostname,
			State:        n.State,
			Availability: n.Availability,
			IsManager:    n.IsManager,
			ObservedAt:   n.ObservedAt.Format("2006-01-02T15:04:05Z07:00"),
			Stale:        n.Stale,
			Labels:       n.Labels,
		}
		if id := n.Labels[state.LabelNodeID]; id != "" {
			v.PlatformID = id
		}
		views = append(views, v)
	}
	if c.jsonOut {
		return writeJSON(env, map[string]any{"nodes": views, "cache": true})
	}
	var b strings.Builder
	b.WriteString("cluster nodes (observation cache; read-only — manage nodes with `docker node`)\n")
	if len(views) == 0 {
		b.WriteString("  (cache empty — fleetlyd observer has not synced yet)\n")
	}
	for _, v := range views {
		id := v.PlatformID
		if id == "" {
			id = "-"
		}
		fmt.Fprintf(&b, "  %s %s state=%s availability=%s manager=%t observed_at=%s",
			id, v.Hostname, v.State, v.Availability, v.IsManager, v.ObservedAt)
		if v.Stale {
			b.WriteString(" STALE")
		}
		b.WriteString("\n")
	}
	_, err = fmt.Fprint(env.Stdout, b.String())
	return err
}

// 编译期断言：嵌套动词与子命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &envCmd{}
	_ commands.Command = &placementCmd{}
	_ commands.Command = &nodesCmd{}
	_ commands.Command = &envSetCmd{}
	_ commands.Flagged = &envSetCmd{}
	_ commands.Command = &envGetCmd{}
	_ commands.Flagged = &envGetCmd{}
	_ commands.Command = &envListCmd{}
	_ commands.Flagged = &envListCmd{}
	_ commands.Command = &envRmCmd{}
	_ commands.Flagged = &envRmCmd{}
	_ commands.Command = &placementShowCmd{}
	_ commands.Flagged = &placementShowCmd{}
	_ commands.Command = &nodesLsCmd{}
	_ commands.Flagged = &nodesLsCmd{}
)
