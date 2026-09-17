// edgefleet 是 edgefleet 平台 CLI 骨架（T0.1）：动词注册用
// lynx-go/commands（D21），随 T2.18 补全命令面并直接消费平台 SDK。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/lynx-go/commands"
)

// version 经构建 -ldflags "-X main.version=..." 注入；未注入时为 dev。
var version = "dev"

type versionCmd struct{}

func (c *versionCmd) Name() string     { return "version" }
func (c *versionCmd) Synopsis() string { return "print the edgefleet CLI version" }
func (c *versionCmd) Usage() string    { return "version" }

func (c *versionCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	_, err := fmt.Fprintf(env.Stdout, "edgefleet %s\n", version)
	return err
}

func main() {
	app := commands.New()
	app.Register(&versionCmd{})

	env := &commands.Environment{Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(app.Run(context.Background(), env, os.Args[1:]))
}
