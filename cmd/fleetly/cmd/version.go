package cmd

import (
	"context"
	"fmt"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/internal/buildinfo"
)

// versionJSON 是 version 动词的 --json 形态（本地合成，无 RPC）。
type versionJSON struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// newVersionCmd 打印 CLI 自身版本（本地命令，不触网）。
func newVersionCmd(info buildinfo.BuildInfo) commands.Command {
	const name = "version"
	return &flaggedVerb{
		name:     name,
		synopsis: "print the CLI version",
		usage:    "version",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(env.Stdout, versionJSON{
					Version: displayVersion(info),
					Commit:  info.Commit,
					Date:    info.Date,
				})
			}
			_, err := fmt.Fprintf(env.Stdout, "fleetly %s\n", displayVersion(info))
			return err
		},
	}
}
