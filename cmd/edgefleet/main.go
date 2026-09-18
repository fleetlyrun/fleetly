package main

import (
	"context"
	"fmt"

	"github.com/lynx-go/commands"
)

// versionCmd 保留 T0.1 的动词形态（print version）。
type versionCmd struct{}

func (c *versionCmd) Name() string     { return "version" }
func (c *versionCmd) Synopsis() string { return "print the edgefleet CLI version" }
func (c *versionCmd) Usage() string    { return "version" }

func (c *versionCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	_, err := fmt.Fprintf(env.Stdout, "edgefleet %s\n", version)
	return err
}
