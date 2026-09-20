package cmd

import (
	"context"
	"flag"
	"fmt"

	"github.com/lynx-go/commands"
)

// versionCmd 保留 T0.1 的动词形态（print version）。--json 输出机器形态
// （T2.18：所有动词支持 --json 纪律）。版本是 CLI 自身版本（构建
// -ldflags "-X main.version=..." 注入，未注入时为 dev）——daemon 版本经
// `fleetly apps ...` 所连控制面的 SystemService.Ping/GetSystemStatus 查询。
type versionCmd struct {
	version string
	jsonOut bool
}

func (c *versionCmd) Name() string     { return "version" }
func (c *versionCmd) Synopsis() string { return "print the fleetly CLI version" }
func (c *versionCmd) Usage() string    { return "version [--json]" }

func (c *versionCmd) SetFlags(fs *flag.FlagSet) {
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *versionCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	if c.jsonOut {
		return writeJSON(env.Stdout, map[string]string{"version": c.version})
	}
	_, err := fmt.Fprintf(env.Stdout, "fleetly %s\n", c.version)
	return err
}
