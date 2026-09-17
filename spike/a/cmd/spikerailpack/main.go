// Spike A driver: railpack v0.39.0 used as a Go library (D6 claim check).
//
// This binary reassembles railwayapp/railpack's own CLI commands from our
// module instead of shelling out to a released railpack binary. If this
// builds and runs, the "Railpack is a reusable Go library" claim holds at
// the version pinned in spike/a/go.mod (v0.39.0). Build behavior
// (BUILDKIT_HOST, docker load, cache flags, secrets hashing) is identical
// to the upstream CLI because the commands are the upstream objects.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/charmbracelet/log"
	"github.com/railwayapp/railpack/cli"
	urfave "github.com/urfave/cli/v3"
)

// version is the railpack version this spike pins. Keep in sync with
// spike/a/go.mod (github.com/railwayapp/railpack). Verified against the
// v0.39.0 tag (2026-09-03).
const version = "v0.39.0"

func main() {
	cli.Version = version

	logger := log.Default()
	logger.SetTimeFormat("")
	urfave.ErrWriter = logger.StandardLog(log.StandardLogOptions{
		ForceLevel: log.ErrorLevel,
	}).Writer()

	commands := []*urfave.Command{
		cli.BuildCommand,
		cli.PrepareCommand,
		cli.InfoCommand,
		cli.PlanCommand,
		cli.SchemaCommand,
	}

	for _, command := range commands {
		command.DisableSliceFlagSeparator = true
	}

	cmd := &urfave.Command{
		Name:     "spikerailpack",
		Usage:    fmt.Sprintf("Spike A driver embedding railwayapp/railpack %s as a library", version),
		Version:  cli.Version,
		Commands: commands,
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
