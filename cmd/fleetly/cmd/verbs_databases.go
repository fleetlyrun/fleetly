package cmd

// Databases 上下文动词（F1.12，ADR-0029）。全部 --json 双形态（golden 钉
// 死）；凭证值永不回显——create 回显 Secret 名（database:<name>），App 经
// process secret_refs 引用后取完整连接 URL。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/lynxland/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func newDatabasesCreateVerb() commands.Command {
	const name = "create"
	var project, engine, version string
	var idem idemKeyFlag
	var restoreFrom string
	var settings multiFlag      // --set key=value（可重复，B2c 首批受理面）
	var cpuMillis, memoryMb int // 0 = 缺省（无限制）
	return &flaggedVerb{
		name: name, synopsis: "Create a database from a template (postgres / pgvector / redis / mysql / mongo)",
		// B3：usage 旗标前置于位置参数（Go flag 首位置参停析——NAME 之后的
		// 旗标会落成多余位置参数被拒；仓律同 skills 守卫）。
		usage: "databases create --project PROJECT_ID --engine ENGINE [--version VERSION] [--set KEY=VALUE]... [--cpu-millis N] [--memory-mb N] [--restore-from-backup BACKUP_ID] [--idempotency-key K] NAME",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "owning project id or name (required)")
			fs.StringVar(&engine, "engine", "", "template engine: postgres | pgvector | redis | mysql | mongo (required)")
			fs.StringVar(&version, "version", "", "engine major version short name (empty = the engine default; list legal values with 'fleetly engines list')")
			fs.Var(&settings, "set", "engine knob KEY=VALUE (repeatable; knob schema per engine: 'fleetly databases config describe ENGINE')")
			fs.IntVar(&cpuMillis, "cpu-millis", 0, "cpu limit in milli-cores (0 = unlimited)")
			fs.IntVar(&memoryMb, "memory-mb", 0, "memory limit in MiB (0 = unlimited)")
			fs.StringVar(&restoreFrom, "restore-from-backup", "",
				"restore this backup id into the new database (engine must match; ADR-0039)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if project == "" || engine == "" {
				return usageErr(name, "--project and --engine are required")
			}
			knobs, err := parseKnobSettings(settings)
			if err != nil {
				return usageErr(name, err.Error())
			}
			var resources *structurev1.DatabaseResources
			if cpuMillis != 0 || memoryMb != 0 {
				// G115：int 旗标值入 int32 字段前显式界检（超界 = 用法错误，
				// 不做静默截断——受理位上限另有执法）。
				if cpuMillis < 0 || cpuMillis > math.MaxInt32 || memoryMb < 0 || memoryMb > math.MaxInt32 {
					return usageErr(name, "--cpu-millis/--memory-mb out of range")
				}
				resources = &structurev1.DatabaseResources{CpuMillis: int32(cpuMillis), MemoryMb: int32(memoryMb)} //nolint:gosec // 界检在上
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				projectID, err := resolveProjectID(ctx, c, project)
				if err != nil {
					return err
				}
				ctx = idem.bind(ctx)
				resp, err := c.Databases.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
					ProjectId: projectID, Name: args[0], Engine: engine, RestoreFromBackup: restoreFrom,
					Version: version, Settings: knobs, Resources: resources,
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
					if s := resp.GetDatabase().GetSettings(); len(s) > 0 {
						// B3：旋钮摘要行（键序排序的 k=v 全集——行值直读回显，
						// 旋钮值非凭证材料）+ 旋钮面 schema 入口。
						_, _ = fmt.Fprintf(env.Stdout, "settings %s (knob schema: fleetly databases config describe %s)\n",
							joinKnobSettings(s), resp.GetDatabase().GetEngine())
					}
					if r := resp.GetDatabase().GetResources(); r != nil {
						_, _ = fmt.Fprintf(env.Stdout, "resources cpu %d millis, memory %d MiB\n", r.GetCpuMillis(), r.GetMemoryMb())
					}
					if restoreFrom != "" {
						_, _ = fmt.Fprintf(env.Stdout, "restore pending from backup %s (watch database.backup_* events)\n", restoreFrom)
					}
					if note := resp.GetRestoreVersionNote(); note != "" {
						_, _ = fmt.Fprintf(env.Stdout, "warning: %s\n", note)
					}
				})
			})
		},
	}
}

// parseKnobSettings 把 --set key=value 旗标面折为请求 map（首见键后见覆
// 写——flag 重复语义；形态不符精确报错）。
func parseKnobSettings(flags multiFlag) (map[string]string, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(flags))
	for _, kv := range flags {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("--set expects KEY=VALUE (got %q)", kv)
		}
		out[key] = value
	}
	return out, nil
}

// databases update（B3，ADR-0095 决策 5/9 + ADR-0089）：settings/resources
// 期望态变更的 CLI 皮——Update 双车道（dynamic 同步热改先行、static 落行
// 后受监督滚动）的人机双形态入口。
//
// 补丁语义（CLI 面）：线上 Update 是声明式全量替换，CLI 读行当前 settings
// 叠加 --set、剔除 --unset 后整集下发——改一个键不清其余键（--set 的字面
// 承诺；Console Settings tab 持有全集表单，直发全量）。资源轴同律：只给
// --cpu-millis 不动内存轴，0 = 该轴无限制（服务端 resourcesCPU/Memory 同
// 口径）。
//
// 确认三形态（ConfirmableVerbs 强制集，铸票面 = PreviewUpdateDatabase）：
// 交互预览 y/N / --preview→--ticket 两调用流 / --yes。两调用流第 2 步须携
// 同一组旗标——票绑合并后期望态摘要，两步之间行被改即 MISMATCH（重跑
// --preview 取新票）。
func newDatabasesUpdateVerb() commands.Command {
	const name = "update"
	var project string
	var sets multiFlag
	var unsets stringSlice
	var cpuMillis, memoryMb optionalInt
	conf := confirmFlags{previewFace: true}
	return &flaggedVerb{
		name:     name,
		synopsis: "Update database knobs and resources (instant knobs take effect live; rolling knobs and resource limits roll the carrier)",
		usage:    "databases update [--project PROJECT] [--set KEY=VALUE]... [--unset KEY]... [--cpu-millis N] [--memory-mb N] [--yes | --preview | --ticket T] DATABASE_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare database name)")
			fs.Var(&sets, "set", "engine knob KEY=VALUE, repeatable (patch: knobs not named keep their current values; schema: 'fleetly databases config describe ENGINE')")
			fs.Var(&unsets, "unset", "remove engine knob KEY so the engine default takes over again, repeatable")
			fs.Var(&cpuMillis, "cpu-millis", "cpu limit in milli-cores (0 = unlimited; omitted = unchanged)")
			fs.Var(&memoryMb, "memory-mb", "memory limit in MiB (0 = unlimited; omitted = unchanged)")
			conf.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			patch, err := parseKnobSettings(sets)
			if err != nil {
				return usageErr(name, err.Error())
			}
			for _, key := range unsets {
				if key == "" {
					return usageErr(name, "--unset expects a knob KEY")
				}
				if _, both := patch[key]; both {
					return usageErr(name, fmt.Sprintf("knob %q is both set and unset (pick one)", key))
				}
			}
			settingsTouched := len(patch) > 0 || len(unsets) > 0
			resourcesTouched := cpuMillis.set || memoryMb.set
			if !settingsTouched && !resourcesTouched {
				return usageErr(name, "nothing to update: pass --set/--unset and/or --cpu-millis/--memory-mb")
			}
			// G115：int 旗标值入 int32 字段前显式界检（create 同文案；受理位
			// 上限另有执法）。
			for _, axis := range []optionalInt{cpuMillis, memoryMb} {
				if axis.set && (axis.value < 0 || axis.value > math.MaxInt32) {
					return usageErr(name, "--cpu-millis/--memory-mb out of range")
				}
			}
			// 确认三形态解析在拨号前（互斥校验与非 TTY 拒绝都是本地面）。
			plan, err := conf.resolve("databases update")
			if err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				dbID, err := resolveDatabaseID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				// 合并基线 = 行当前期望态（补丁语义的读半边）。
				current, err := c.Databases.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: dbID})
				if err != nil {
					return err
				}
				before := current.GetDatabase()
				req := &structurev1.UpdateDatabaseRequest{DatabaseId: dbID}
				if settingsTouched {
					req.Settings = &structurev1.DatabaseSettingsUpdate{
						Values: mergeKnobSettings(before.GetSettings(), patch, unsets),
					}
				}
				if resourcesTouched {
					req.Resources = mergeResources(before.GetResources(), cpuMillis, memoryMb)
				}
				// 确认前半：--preview 打印即退出；交互默认打印预演后 y/N——
				// y 以预演票带票直发，N 中止（零写请求）。
				if plan.mode == confirmPreviewOnly || plan.mode == confirmInteractive {
					preview, perr := c.Databases.PreviewUpdateDatabase(ctx, &structurev1.PreviewUpdateDatabaseRequest{Update: req})
					if perr != nil {
						return perr
					}
					if rerr := renderUpdatePreview(env, jsonOut, args[0], preview); rerr != nil {
						return rerr
					}
					if plan.mode == confirmPreviewOnly {
						return nil
					}
					confirmed, cerr := promptConfirm(env, "Update database")
					if cerr != nil {
						return cerr
					}
					if !confirmed {
						_, _ = fmt.Fprintln(env.Stdout, "aborted (nothing was sent)")
						return nil
					}
					req.Confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
				} else {
					req.Confirmation = plan.confirmation()
					if plan.mode == confirmSkip && !jsonOut {
						_, _ = fmt.Fprintln(env.Stdout, confirmSkipNote)
					}
				}
				resp, err := c.Databases.UpdateDatabase(ctx, req)
				if err != nil {
					return presentConfirmError(err)
				}
				return renderOut(env, jsonOut, resp, func() { renderUpdateResult(env, before, resp) })
			})
		},
	}
}

// optionalInt 是「显式给出才算数」的整数旗标（update 补丁语义的资源轴：
// 未给 = 该轴不动；给 0 = 该轴无限制）。值类型错误经 flag 包上抛为用法
// 错误（exit 64）。
type optionalInt struct {
	value int
	set   bool
}

func (o *optionalInt) String() string {
	if o == nil || !o.set {
		return ""
	}
	return strconv.Itoa(o.value)
}

func (o *optionalInt) Set(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("expected an integer (got %q)", v)
	}
	o.value, o.set = n, true
	return nil
}

// mergeKnobSettings 是 settings 的补丁合并（行当前值 ∪ --set 覆写 − --unset
// 剔除）：产出整集——Update 线上语义是全量替换，合并在 CLI 侧完成。剔除
// 未设置的键是声明式 no-op（脚本可幂等重跑）。
func mergeKnobSettings(current, patch map[string]string, unset []string) map[string]string {
	out := make(map[string]string, len(current)+len(patch))
	for k, v := range current {
		out[k] = v
	}
	for k, v := range patch {
		out[k] = v
	}
	for _, k := range unset {
		delete(out, k)
	}
	return out
}

// mergeResources 是资源轴的补丁合并：显式给出的轴覆写，未给的轴沿用行
// 当前值（缺席消息 = 双轴无限制 = 0）。界检在调用方。
func mergeResources(current *structurev1.DatabaseResources, cpu, memory optionalInt) *structurev1.DatabaseResources {
	next := &structurev1.DatabaseResources{CpuMillis: current.GetCpuMillis(), MemoryMb: current.GetMemoryMb()}
	if cpu.set {
		next.CpuMillis = int32(cpu.value) //nolint:gosec // 界检在调用方（run 前置）
	}
	if memory.set {
		next.MemoryMb = int32(memory.value) //nolint:gosec // 界检在调用方（run 前置）
	}
	return next
}

// knobClassBadge 把目录 class 词面（static | dynamic）压成用户面徽标
// （rolling | instant——design §3.5；Console 徽标同词）。
func knobClassBadge(class string) string {
	switch class {
	case "dynamic":
		return "instant"
	case "static":
		return "rolling"
	default:
		return class
	}
}

// knobValueOrDefault 渲染 diff 端点值（空串 = 原缺席/本次移除 = 引擎缺省
// 生效，proto 注释同义）。
func knobValueOrDefault(v string) string {
	if v == "" {
		return "(default)"
	}
	return v
}

// describeResources 渲染资源面（缺席轴/0 = unlimited）。
func describeResources(r *structurev1.DatabaseResources) string {
	cpu, memory := "unlimited", "unlimited"
	if v := r.GetCpuMillis(); v > 0 {
		cpu = fmt.Sprintf("%d millis", v)
	}
	if v := r.GetMemoryMb(); v > 0 {
		memory = fmt.Sprintf("%d MiB", v)
	}
	return "cpu " + cpu + ", memory " + memory
}

// keysOrNone 渲染键集（空 = none）。
func keysOrNone(keys []string) string {
	if len(keys) == 0 {
		return "none"
	}
	return strings.Join(keys, ", ")
}

// renderUpdatePreview 渲染 Update 预演载荷（人类/机器双形态，render 单点
// 约定）：旋钮级 diff（逐键带 instant/rolling 徽标）/ 资源面 diff / 车道
// 判定 / 滚动后果（replace 时附「分次 update = 分次重启」提示——红队
// A12/A14 记档项随 B3 入 CLI 面）；ticket 行殿后（renderDeployPreview 同
// 形——确认的正是展示的）。机器形态 = PreviewUpdateDatabaseResponse
// protojson（ticket 与 ticket_expires_in native 携带）。
func renderUpdatePreview(env *commands.Environment, jsonOut bool, database string, resp *structurev1.PreviewUpdateDatabaseResponse) error {
	if jsonOut {
		return writeProtoJSON(env.Stdout, resp)
	}
	return renderUpdatePreviewBody(env, "database "+database,
		resp.GetSettingsChanges(), resp.GetResourcesFrom(), resp.GetResourcesTo(),
		resp.GetDynamicKeys(), resp.GetStaticKeys(), resp.GetRollout(), resp.GetTicket(), resp.GetTicketExpiresIn())
}

// renderUpdatePreviewBody 是两类型 Update 预演的共享渲染体（B4c 三种数据服务类型对齐
// ——diff/车道/滚动后果/ticket 的文案单源；heading = 「<kind> <目标>」）。
func renderUpdatePreviewBody(env *commands.Environment, heading string,
	changes []*structurev1.DatabaseSettingChange, resFrom, resTo *structurev1.DatabaseResources,
	dynamicKeys, staticKeys []string, rollout, ticket string, ticketExpiresIn int32) error {
	lines := []string{"update preview for " + heading}
	if len(changes) == 0 {
		lines = append(lines, "settings changes: none")
	} else {
		lines = append(lines, fmt.Sprintf("settings changes (%d):", len(changes)))
		for _, ch := range changes {
			lines = append(lines, fmt.Sprintf("  %s: %s -> %s [%s]", ch.GetKey(),
				knobValueOrDefault(ch.GetFrom()), knobValueOrDefault(ch.GetTo()), knobClassBadge(ch.GetClass())))
		}
	}
	if from, to := describeResources(resFrom), describeResources(resTo); from == to {
		lines = append(lines, "resources: unchanged ("+from+")")
	} else {
		lines = append(lines, "resources: "+from+" -> "+to)
	}
	lines = append(lines,
		"instant lane (takes effect live, no restart): "+keysOrNone(dynamicKeys),
		"rolling lane (supervised rolling restart): "+keysOrNone(staticKeys))
	switch rollout {
	case "zero_touch":
		lines = append(lines, "rollout: zero_touch (the carrier keeps running; nothing restarts)")
	case "replace":
		lines = append(lines,
			"rollout: replace (rolling knobs or resource limits changed: the carrier rolls once - expect a brief connection blip)",
			"tip: batch rolling changes into one update - each update that carries rolling changes restarts the carrier once")
	default:
		lines = append(lines, "rollout: "+rollout)
	}
	lines = append(lines, fmt.Sprintf("ticket: %s (expires in %ds)", ticket, ticketExpiresIn))
	_, err := fmt.Fprintln(env.Stdout, strings.Join(lines, "\n"))
	return err
}

// renderUpdateResult 渲染 Update 回执（人类形态）：两车道落点（instant 已
// 生效 / rolling 已落行待滚动）+ 资源面变化 + 期望态全集回显（键序排序——
// map 遍历序不进输出）。
func renderUpdateResult(env *commands.Environment, before *structurev1.Database, resp *structurev1.UpdateDatabaseResponse) {
	d := resp.GetDatabase()
	lines := []string{fmt.Sprintf("updated database %s (id %s)", d.GetName(), d.GetId())}
	resourcesChanged := describeResources(before.GetResources()) != describeResources(d.GetResources())
	if len(resp.GetDynamicApplied()) > 0 {
		lines = append(lines, "instant knobs now in effect (no restart): "+strings.Join(resp.GetDynamicApplied(), ", "))
	}
	if len(resp.GetStaticApplied()) > 0 {
		lines = append(lines, "rolling knobs saved (the carrier rolls to pick them up): "+strings.Join(resp.GetStaticApplied(), ", "))
	}
	if resourcesChanged {
		lines = append(lines, "resources now "+describeResources(d.GetResources())+" (the carrier rolls to pick them up)")
	}
	if len(resp.GetDynamicApplied()) == 0 && len(resp.GetStaticApplied()) == 0 && !resourcesChanged {
		lines = append(lines, "no changes (the request matches the current desired state)")
	}
	if s := d.GetSettings(); len(s) == 0 {
		lines = append(lines, "settings: none (engine defaults)")
	} else {
		lines = append(lines, "settings: "+joinKnobSettings(s))
	}
	if len(resp.GetStaticApplied()) > 0 || resourcesChanged {
		lines = append(lines, "watch the roll with 'fleetly databases get' until status is running again")
	}
	_, _ = fmt.Fprintln(env.Stdout, strings.Join(lines, "\n"))
}

// joinKnobSettings 渲染旋钮期望态全集（键序排序的 k=v，逗号分隔——map
// 遍历序不进输出，golden 确定性）。
func joinKnobSettings(settings map[string]string) string {
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+settings[k])
	}
	return strings.Join(pairs, ", ")
}

func newDatabasesListVerb() commands.Command {
	const name = "list"
	var project string
	var pages pageFlags // 游标分页旗标收敛共享 struct（P4-2 批 A 全量迁移）
	return &flaggedVerb{
		name: name, synopsis: "List databases in a project (newest first)",
		usage: "databases list --project PROJECT_ID [--after SEQ] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (required)")
			pages.register(fs, "after")
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
				resp, err := c.Databases.ListDatabases(ctx, &structurev1.ListDatabasesRequest{
					ProjectId: projectID, AfterDatabaseSeq: pages.cursor(), Limit: pages.limit32(),
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintln(env.Stdout, "NAME\tID\tENGINE\tVERSION\tSTATUS\tCREDENTIALS\tCREATED")
					for _, d := range resp.GetDatabases() {
						_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
							d.GetId(), d.GetName(), d.GetEngine(), d.GetVersion(), d.GetStatus(), d.GetCredentialsRef(), d.GetCreatedAt())
					}
				})
			})
		},
	}
}

func newDatabasesGetVerb() commands.Command {
	const name = "get"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Show one database (connection face without credentials)",
		usage: "databases get [--project PROJECT] DATABASE_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare database name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				dbID, err := resolveDatabaseID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Databases.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: dbID})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetDatabase(), func() {
					d := resp.GetDatabase()
					_, _ = fmt.Fprintf(env.Stdout,
						"id %s\nname %s\nengine %s %s\nstatus %s\ncredential secret %s (value never shown)\nin-network endpoint %s:%d\ncreated %s\n",
						d.GetId(), d.GetName(), d.GetEngine(), d.GetVersion(), d.GetStatus(),
						d.GetCredentialsRef(), d.GetHost(), d.GetPort(), d.GetCreatedAt())
					// B3：期望态旋钮/资源面在场才回显（update 补丁语义的读面；
					// 缺席 = 引擎缺省/无限制，行不出现）。
					if s := d.GetSettings(); len(s) > 0 {
						_, _ = fmt.Fprintf(env.Stdout, "settings %s\n", joinKnobSettings(s))
					}
					if r := d.GetResources(); r != nil {
						_, _ = fmt.Fprintf(env.Stdout, "resources %s\n", describeResources(r))
					}
				})
			})
		},
	}
}

// databases delete（ADR-0029 决策 8）：收口删除——拆载体 + tombstone；
// 数据卷与凭证 Secret 残留（Project 级材料，备份保留义）。
func newDatabasesDeleteVerb() commands.Command {
	const name = "delete"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Delete a database (tears down carriers; volume and credential secret are retained)",
		usage: "databases delete [--project PROJECT] DATABASE_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare database name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				dbID, err := resolveDatabaseID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Databases.DeleteDatabase(ctx, &structurev1.DeleteDatabaseRequest{Id: dbID})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintf(env.Stdout,
						"deleted database %s (carriers torn down; data volume and credential secret retained)\n", args[0])
				})
			})
		},
	}
}

// databases backup（F2.2，ADR-0039）：手动触发——铸台账行即返，完成事实
// 由事件与 backups 列表观测（备份时长无上界承诺，同步等待面不成立）。
func newDatabasesBackupVerb() commands.Command {
	const name = "backup"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Trigger a backup now (returns a pending ledger row; watch database.backup_* events)",
		usage: "databases backup [--project PROJECT] DATABASE_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare database name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				dbID, err := resolveDatabaseID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Databases.TriggerBackup(ctx, &structurev1.TriggerBackupRequest{DatabaseId: dbID})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetBackup(), func() {
					b := resp.GetBackup()
					_, _ = fmt.Fprintf(env.Stdout,
						"backup triggered (id %s) status %s\ncompletion lands in database.backup_succeeded / database.backup_failed events\n",
						b.GetId(), b.GetStatus())
				})
			})
		},
	}
}

// databases backups（F2.2，ADR-0039）：台账列举，新→旧分页。
func newDatabasesBackupsVerb() commands.Command {
	const name = "backups"
	var project string
	var pages pageFlags // 游标分页旗标收敛共享 struct（P4-2 批 A 全量迁移）
	return &flaggedVerb{
		name: name, synopsis: "List backups of a database (newest first)",
		usage: "databases backups [--project PROJECT] DATABASE_ID [--after SEQ] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare database name)")
			pages.register(fs, "after")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				dbID, err := resolveDatabaseID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Databases.ListBackups(ctx, &structurev1.ListBackupsRequest{
					DatabaseId: dbID, AfterBackupSeq: pages.cursor(), Limit: pages.limit32(),
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintln(env.Stdout, "ID\tSTATUS\tENGINE\tSIZE\tDIGEST\tOBJECT\tFINISHED")
					for _, b := range resp.GetBackups() {
						_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
							b.GetId(), b.GetStatus(), b.GetEngine(), b.GetSizeBytes(), b.GetDigest(), b.GetObjectKey(), b.GetFinishedAt())
					}
				})
			})
		},
	}
}

// databases verify（F2.2，ADR-0039 决策 8）：重算对象摘要比对回执。
func newDatabasesVerifyVerb() commands.Command {
	const name = "verify"
	return &flaggedVerb{
		name: name, synopsis: "Verify a backup object (recomputes sha256 against the ledger receipt)",
		usage: "databases verify BACKUP_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BACKUP_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.Databases.VerifyBackup(ctx, &structurev1.VerifyBackupRequest{BackupId: args[0]})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					if resp.GetOk() {
						_, _ = fmt.Fprintf(env.Stdout, "backup %s verified (digest %s matches the ledger receipt)\n",
							args[0], resp.GetDigest())
						return
					}
					_, _ = fmt.Fprintf(env.Stdout, "backup %s FAILED verification: %s\n", args[0], resp.GetError())
				})
			})
		},
	}
}

// databases browse（F3.6，ADR-0051）：铸造数据浏览器会话——打印入口 URL
// （含一次性 Launcher Ticket，120s 单用途；兑换即烧，过期重开）。不自动开
// 浏览器：CLI 面以脚本消费为主（URL 是唯一交付物）。
// databases download-backup（IA v3 二期⑤）：流式下载备份对象到本地文件
// （分块重组；--out 必填——不落 stdout，二进制不进管道文本面）。
func newDatabasesDownloadBackupVerb() commands.Command {
	const name = "download-backup"
	var project, database, out string
	return &flaggedVerb{
		name:     name,
		synopsis: "Download a succeeded backup object to a local file (streamed in chunks)",
		usage:    "databases download-backup --database DATABASE_ID [--project PROJECT] BACKUP_ID --out PATH",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&database, "database", "", "owning database id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare database name)")
			fs.StringVar(&out, "out", "", "output file path (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BACKUP_ID argument")
			}
			if database == "" {
				return usageErr(name, "--database is required")
			}
			if out == "" {
				return usageErr(name, "--out is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				dbID, err := resolveDatabaseID(ctx, c, project, database)
				if err != nil {
					return err
				}
				stream, err := c.Databases.DownloadBackup(ctx, &structurev1.DownloadBackupRequest{
					DatabaseId: dbID, BackupId: args[0],
				})
				if err != nil {
					return err
				}
				// 截断语义：先清目标文件（追加写只发生在本次下载窗口内）。
				if err := os.WriteFile(out, []byte{}, 0o600); err != nil {
					return err
				}
				var size int64
				for {
					frame, rerr := stream.Recv()
					if rerr == io.EOF {
						break
					}
					if rerr != nil {
						return rerr
					}
					if err := appendFile(out, frame.GetData()); err != nil {
						return err
					}
					size += int64(len(frame.GetData()))
				}
				if jsonOut {
					data, merr := json.Marshal(&downloadSummary{Database: dbID, Backup: args[0], Path: out, Bytes: size})
					if merr != nil {
						return merr
					}
					_, _ = fmt.Fprintln(env.Stdout, string(data))
					return nil
				}
				_, _ = fmt.Fprintf(env.Stdout, "downloaded %s (%d bytes) → %s\n", args[0], size, out)
				return nil
			})
		},
	}
}

// downloadSummary 是 --json 形态的下载回执。
type downloadSummary struct {
	Database string `json:"database"`
	Backup   string `json:"backup"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
}

// databases rotate-password（IA v3 二期⑤b）：方言级凭证轮换——新连接串
// 只进凭证 Secret（值永不回显）；回执显式披露级联语义（引用库的 App 持
// 旧值直到重新部署）。位置参数沿 get/delete 惯例。
func newDatabasesRotatePasswordVerb() commands.Command {
	const name = "rotate-password"
	var project string
	return &flaggedVerb{
		name:     name,
		synopsis: "Rotate the database credential (a new connection URL lands in the credential secret)",
		usage:    "databases rotate-password [--project PROJECT] DATABASE_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare database name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				dbID, err := resolveDatabaseID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Databases.RotateDatabasePassword(ctx, &structurev1.RotateDatabasePasswordRequest{DatabaseId: dbID})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetDatabase(), func() {
					d := resp.GetDatabase()
					_, _ = fmt.Fprintf(env.Stdout,
						"rotated credential for database %s (id %s)\ncredential secret %s now holds the new connection URL (value never shown)\ncascade: apps referencing this database keep the old credential until redeployed - redeploy them now\n",
						d.GetName(), d.GetId(), d.GetCredentialsRef())
				})
			})
		},
	}
}

// appendFile 以追加语义落盘（先截断在首次写入前由调用方保证——此处
// 首帧前 O_TRUNC 由 downloadVerb 预清理实现）。
func appendFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304：path 是用户 --out 旗标（本地落盘即目的），非不可信包含
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(data)
	return err
}

func newDatabasesBrowseVerb() commands.Command {
	const name = "browse"
	var project string
	var write bool
	return &flaggedVerb{
		name: name, synopsis: "Open a data-browser session for a database (prints a one-time entry URL)",
		usage: "databases browse [--project PROJECT] DATABASE_ID [--write]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare database name)")
			fs.BoolVar(&write, "write", false, "unlock the write mode (requires the databases:write scope; default is read-only)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				dbID, err := resolveDatabaseID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Databases.BrowseDatabase(ctx, &structurev1.BrowseDatabaseRequest{
					DatabaseId: dbID, ReadWrite: write,
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintf(env.Stdout,
						"browse session %s opened (%s, read-only %t, enforcement %s)\nopen within %d seconds (one-time ticket):\n%s\n",
						resp.GetSessionId(), resp.GetBrowser(), resp.GetReadOnly(),
						shortEnforcement(resp.GetEnforcement()), resp.GetExpiresIn(), resp.GetUrl())
				})
			})
		},
	}
}

// shortEnforcement 把执法层级枚举压成人类短形（session/tool/none——proto
// 枚举全名是 wire 面，终端面用词值）。
func shortEnforcement(e structurev1.BrowseReadOnlyEnforcement) string {
	switch e {
	case structurev1.BrowseReadOnlyEnforcement_BROWSE_READ_ONLY_ENFORCEMENT_SESSION:
		return "session"
	case structurev1.BrowseReadOnlyEnforcement_BROWSE_READ_ONLY_ENFORCEMENT_TOOL:
		return "tool"
	default:
		return "none"
	}
}
