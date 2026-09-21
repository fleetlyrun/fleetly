package cmd

// 库实例命令（E4 数据库托管，W4-S2 生命周期面）：`fleetly databases
// <create|get|list|delete|suspend|resume|retry|settings>`。全部经 RPC
//（CLI-over-SDK 纪律）；连接信息为服务端脱敏投影——密码明文零出现
//（掩码 URL + 指纹），显式 reveal 不在 CLI 面（S4/S6）。删除在 CLI 侧
// 自动回传 confirm=<名>（REST 面保持显式 confirm 参数）；--delete-volumes
// 是唯一需要显式 flag 的数据破坏性选择。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// databasesCmd 是外层动词 `databases`：分发生命周期子命令。
type databasesCmd struct {
	sub *commands.App
}

func newDatabasesCmd() *databasesCmd {
	sub := commands.New()
	sub.Register(&databaseCreateCmd{}, &databaseGetCmd{}, &databaseListCmd{}, &databaseDeleteCmd{},
		&databaseSuspendCmd{}, &databaseResumeCmd{}, &databaseRetryCmd{}, &databaseSettingsCmd{})
	sub.VerbTitle = "databases subcommands:"
	return &databasesCmd{sub: sub}
}

func (c *databasesCmd) Name() string { return "databases" }
func (c *databasesCmd) Synopsis() string {
	return "create and manage managed database instances (lifecycle, settings; connection secrets are masked)"
}
func (c *databasesCmd) Usage() string {
	return "databases <create|get|list|delete|suspend|resume|retry|settings> [flags] ..."
}

func (c *databasesCmd) SetFlags(_ *flag.FlagSet) {}

func (c *databasesCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (create|get|list|delete|suspend|resume|retry|settings)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// databaseLimitFlags 是 create/settings 共享的限额与备份计划 flag 集（零值 =
// 模板/平台缺省）。
type databaseLimitFlags struct {
	cpu           float64
	memoryBytes   int64
	backupHours   int
	backupKeep    int
	backupHourUTC int
}

func (f *databaseLimitFlags) register(fs *flag.FlagSet) {
	fs.Float64Var(&f.cpu, "cpu", 0, "CPU limit in cores (0 = template default)")
	fs.Int64Var(&f.memoryBytes, "memory-bytes", 0, "memory limit in bytes (0 = template default)")
	fs.IntVar(&f.backupHours, "backup-interval-hours", 0, "backup interval in hours (0 = platform default 24)")
	fs.IntVar(&f.backupKeep, "backup-keep", 0, "backups to keep (0 = platform default 7)")
	fs.IntVar(&f.backupHourUTC, "backup-hour-utc", 0, "daily backup hour in UTC (0 = platform default 3)")
}

func (f *databaseLimitFlags) limits() *serverv1.DatabaseLimits {
	if f.cpu == 0 && f.memoryBytes == 0 {
		return nil
	}
	return &serverv1.DatabaseLimits{CpuSeconds: f.cpu, MemoryBytes: f.memoryBytes}
}

func (f *databaseLimitFlags) backupPlan() *serverv1.DatabaseBackupPlan {
	if f.backupHours == 0 && f.backupKeep == 0 && f.backupHourUTC == 0 {
		return nil
	}
	return &serverv1.DatabaseBackupPlan{
		IntervalHours: int32(f.backupHours),
		Keep:          int32(f.backupKeep),
		HourUtc:       int32(f.backupHourUTC),
	}
}

// databaseCreateCmd 实现 `fleetly databases create <name> <template>`。
type databaseCreateCmd struct {
	template string
	jsonOut  bool
	limits   databaseLimitFlags
	conn     connFlags
}

func (c *databaseCreateCmd) Name() string { return "create" }
func (c *databaseCreateCmd) Synopsis() string {
	return "create a managed database instance (accepted as provisioning; the platform converges it to ready)"
}
func (c *databaseCreateCmd) Usage() string {
	return "databases create [--template postgres-16|redis-7] [--cpu ...] [--memory-bytes ...] [--addr <host:port>] [--token <tok>] [--json] <name>"
}

func (c *databaseCreateCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.template, "template", "postgres-16", "engine template id (postgres-16 or redis-7)")
	c.limits.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *databaseCreateCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Databases().CreateDatabase(ctx, &serverv1.CreateDatabaseRequest{
			Name:       args[0],
			Template:   c.template,
			Limits:     c.limits.limits(),
			BackupPlan: c.limits.backupPlan(),
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, toDatabaseJSON(resp.GetDatabase()))
		}
		_, err = fmt.Fprint(env.Stdout, renderDatabase(resp.GetDatabase()))
		return err
	})
}

// databaseGetCmd 实现 `fleetly databases get <name>`。
type databaseGetCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *databaseGetCmd) Name() string { return "get" }
func (c *databaseGetCmd) Synopsis() string {
	return "show one database instance (connection values are masked; use the referencing app env for the live credential)"
}
func (c *databaseGetCmd) Usage() string {
	return "databases get [--addr <host:port>] [--token <tok>] [--json] <name>"
}

func (c *databaseGetCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *databaseGetCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Databases().GetDatabase(ctx, &serverv1.GetDatabaseRequest{Name: args[0]})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, toDatabaseJSON(resp.GetDatabase()))
		}
		_, err = fmt.Fprint(env.Stdout, renderDatabase(resp.GetDatabase()))
		return err
	})
}

// databaseListCmd 实现 `fleetly databases list`。
type databaseListCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *databaseListCmd) Name() string { return "list" }
func (c *databaseListCmd) Synopsis() string {
	return "list database instances (name ascending; deleted tombstones are not listed)"
}
func (c *databaseListCmd) Usage() string {
	return "databases list [--addr <host:port>] [--token <tok>] [--json]"
}

func (c *databaseListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *databaseListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Databases().ListDatabases(ctx, &serverv1.ListDatabasesRequest{})
		if err != nil {
			return err
		}
		if c.jsonOut {
			rows := make([]databaseJSON, 0, len(resp.GetDatabases()))
			for _, v := range resp.GetDatabases() {
				rows = append(rows, toDatabaseJSON(v))
			}
			return writeJSON(env.Stdout, struct {
				Databases []databaseJSON `json:"databases"`
			}{rows})
		}
		if len(resp.GetDatabases()) == 0 {
			_, err := fmt.Fprintln(env.Stdout, "no database instances (create one with: fleetly databases create <name> --template <id>)")
			return err
		}
		for _, v := range resp.GetDatabases() {
			line := fmt.Sprintf("%s  %-13s %-12s %s", v.GetName(), v.GetStatus(), v.GetTemplate(), v.GetPlacement())
			if le := v.GetLastError(); le != "" {
				line += "  error=" + le
			}
			if _, err := fmt.Fprintln(env.Stdout, line); err != nil {
				return err
			}
		}
		return nil
	})
}

// databaseDeleteCmd 实现 `fleetly databases delete <name> [--delete-volumes]`。
type databaseDeleteCmd struct {
	deleteVolumes bool
	conn          connFlags
}

func (c *databaseDeleteCmd) Name() string { return "delete" }
func (c *databaseDeleteCmd) Synopsis() string {
	return "delete a database instance (tombstone; data volume is KEPT and orphaned unless --delete-volumes is passed)"
}
func (c *databaseDeleteCmd) Usage() string {
	return "databases delete [--delete-volumes] [--addr <host:port>] [--token <tok>] <name>"
}

func (c *databaseDeleteCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.deleteVolumes, "delete-volumes", false, "irreversibly delete the data volume (default: keep it as orphaned)")
}

func (c *databaseDeleteCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Databases().DeleteDatabase(ctx, &serverv1.DeleteDatabaseRequest{
			Name:          args[0],
			Confirm:       args[0],
			DeleteVolumes: c.deleteVolumes,
		})
		if err != nil {
			return err
		}
		note := "the data volume is kept as orphaned"
		if c.deleteVolumes {
			note = "the data volume is being deleted irreversibly"
		}
		_, err = fmt.Fprintf(env.Stdout, "database %s accepted for deletion (%s); the platform reaps managed objects in the background; %s\n",
			resp.GetName(), resp.GetStatus(), note)
		return err
	})
}

// databaseSuspendCmd 实现 `fleetly databases suspend <name>`。
type databaseSuspendCmd struct {
	conn connFlags
}

func (c *databaseSuspendCmd) Name() string { return "suspend" }
func (c *databaseSuspendCmd) Synopsis() string {
	return "suspend a database (scale to zero; services and volumes are retained, referencing apps lose connectivity)"
}
func (c *databaseSuspendCmd) Usage() string {
	return "databases suspend [--addr <host:port>] [--token <tok>] <name>"
}

func (c *databaseSuspendCmd) SetFlags(fs *flag.FlagSet) { c.conn.register(fs) }

func (c *databaseSuspendCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Databases().SuspendDatabase(ctx, &serverv1.SuspendDatabaseRequest{Name: args[0]})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(env.Stdout, "database %s suspended (status %s)\n", args[0], resp.GetDatabase().GetStatus())
		return err
	})
}

// databaseResumeCmd 实现 `fleetly databases resume <name>`。
type databaseResumeCmd struct {
	conn connFlags
}

func (c *databaseResumeCmd) Name() string { return "resume" }
func (c *databaseResumeCmd) Synopsis() string {
	return "resume a suspended database (reconverges to provisioning, then ready/degraded)"
}
func (c *databaseResumeCmd) Usage() string {
	return "databases resume [--addr <host:port>] [--token <tok>] <name>"
}

func (c *databaseResumeCmd) SetFlags(fs *flag.FlagSet) { c.conn.register(fs) }

func (c *databaseResumeCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Databases().ResumeDatabase(ctx, &serverv1.ResumeDatabaseRequest{Name: args[0]})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(env.Stdout, "database %s resumed (status %s); reconvergence in progress\n", args[0], resp.GetDatabase().GetStatus())
		return err
	})
}

// databaseRetryCmd 实现 `fleetly databases retry <name>`。
type databaseRetryCmd struct {
	conn connFlags
}

func (c *databaseRetryCmd) Name() string { return "retry" }
func (c *databaseRetryCmd) Synopsis() string {
	return "retry a failed database (failed -> provisioning reconvergence; the failed scene is preserved until then)"
}
func (c *databaseRetryCmd) Usage() string {
	return "databases retry [--addr <host:port>] [--token <tok>] <name>"
}

func (c *databaseRetryCmd) SetFlags(fs *flag.FlagSet) { c.conn.register(fs) }

func (c *databaseRetryCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Databases().RetryDatabase(ctx, &serverv1.RetryDatabaseRequest{Name: args[0]})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(env.Stdout, "database %s retry accepted (status %s); reconvergence in progress\n", args[0], resp.GetDatabase().GetStatus())
		return err
	})
}

// databaseSettingsCmd 实现 `fleetly databases settings <name> [flags]`。
type databaseSettingsCmd struct {
	limits  databaseLimitFlags
	jsonOut bool
	conn    connFlags
}

func (c *databaseSettingsCmd) Name() string { return "settings" }
func (c *databaseSettingsCmd) Synopsis() string {
	return "update limits and backup plan (whole replacement; limits are applied by the next convergence beat)"
}
func (c *databaseSettingsCmd) Usage() string {
	return "databases settings [--cpu ...] [--memory-bytes ...] [--backup-interval-hours ...] [--addr <host:port>] [--token <tok>] [--json] <name>"
}

func (c *databaseSettingsCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	c.limits.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *databaseSettingsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Databases().UpdateDatabaseSettings(ctx, &serverv1.UpdateDatabaseSettingsRequest{
			Name:       args[0],
			Limits:     c.limits.limits(),
			BackupPlan: c.limits.backupPlan(),
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, toDatabaseJSON(resp.GetDatabase()))
		}
		_, err = fmt.Fprintf(env.Stdout, "settings updated for %s (cpu=%v memory=%d status=%s)\n",
			args[0], resp.GetDatabase().GetLimits().GetCpuSeconds(),
			resp.GetDatabase().GetLimits().GetMemoryBytes(), resp.GetDatabase().GetStatus())
		return err
	})
}

// renderDatabase 是详情的人读形态（连接段恒为掩码口径）。
func renderDatabase(v *serverv1.DatabaseView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "database %s\n", v.GetName())
	fmt.Fprintf(&b, "  id: %s\n  template: %s\n  status: %s\n", v.GetId(), v.GetTemplate(), v.GetStatus())
	fmt.Fprintf(&b, "  image: %s\n", v.GetImageDigest())
	fmt.Fprintf(&b, "  placement: %s\n", v.GetPlacement())
	if vol := v.GetVolume(); vol != nil {
		fmt.Fprintf(&b, "  volume: %s (%s)\n", vol.GetName(), vol.GetStatus())
	} else {
		b.WriteString("  volume: (not registered yet)\n")
	}
	if conn := v.GetConnection(); conn != nil {
		fmt.Fprintf(&b, "  connection: %s\n", conn.GetUrl())
		fmt.Fprintf(&b, "  password: masked (fingerprint %s)\n", conn.GetPasswordFingerprint())
	}
	limits := v.GetLimits()
	fmt.Fprintf(&b, "  limits: cpu=%v memory=%d\n", limits.GetCpuSeconds(), limits.GetMemoryBytes())
	backup := v.GetBackupPlan()
	fmt.Fprintf(&b, "  backup: interval=%dh keep=%d hour_utc=%d\n",
		backup.GetIntervalHours(), backup.GetKeep(), backup.GetHourUtc())
	if v.GetLastError() != "" {
		fmt.Fprintf(&b, "  last_error: %s\n", v.GetLastError())
	}
	return b.String()
}

// databaseJSON 是库实例的机器可读投影（时间 RFC3339；连接只带掩码形态）。
type databaseJSON struct {
	ID                  string                           `json:"id"`
	Name                string                           `json:"name"`
	Template            string                           `json:"template"`
	Status              string                           `json:"status"`
	ImageDigest         string                           `json:"image_digest"`
	Placement           string                           `json:"placement,omitempty"`
	Volume              *serverv1.DatabaseVolumeView     `json:"volume,omitempty"`
	Connection          *serverv1.DatabaseConnectionView `json:"connection,omitempty"`
	Limits              *serverv1.DatabaseLimits         `json:"limits,omitempty"`
	BackupPlan          *serverv1.DatabaseBackupPlan     `json:"backup_plan,omitempty"`
	CredentialUpdatedAt string                           `json:"credential_updated_at,omitempty"`
	LastError           string                           `json:"last_error,omitempty"`
	CreatedAt           string                           `json:"created_at,omitempty"`
	UpdatedAt           string                           `json:"updated_at,omitempty"`
}

func toDatabaseJSON(v *serverv1.DatabaseView) databaseJSON {
	out := databaseJSON{
		ID:          v.GetId(),
		Name:        v.GetName(),
		Template:    v.GetTemplate(),
		Status:      v.GetStatus(),
		ImageDigest: v.GetImageDigest(),
		Placement:   v.GetPlacement(),
		Volume:      v.GetVolume(),
		Connection:  v.GetConnection(),
		Limits:      v.GetLimits(),
		BackupPlan:  v.GetBackupPlan(),
		LastError:   v.GetLastError(),
	}
	if ts := v.GetCredentialUpdatedAt(); ts != nil {
		out.CredentialUpdatedAt = tstampRFC3339(ts)
	}
	if ts := v.GetCreatedAt(); ts != nil {
		out.CreatedAt = tstampRFC3339(ts)
	}
	if ts := v.GetUpdatedAt(); ts != nil {
		out.UpdatedAt = tstampRFC3339(ts)
	}
	return out
}

// 编译期断言：databases 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &databasesCmd{}
	_ commands.Flagged = &databasesCmd{}
)
