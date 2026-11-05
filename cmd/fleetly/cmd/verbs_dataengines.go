package cmd

// engines 命令组（B2c，ADR-0094 决策 4 / ADR-0095 决策 10）：Engine Catalog
// 的 CLI 取值面——引擎/在册版本（默认与弃用）/旋钮 schema/能力，全部从
// 服务端目录 RPC 现场取值（硬编码引擎常量退役路径的读半边；Console 目录
// 驱动表单随 B3）。双形态 golden 钉死。

import (
	"context"
	"fmt"
	"strings"

	"github.com/lynxland/commands"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// newEnginesListVerb 是目录列举（READ 面；值域单源 = 服务端模板注册表投影）。
func newEnginesListVerb() commands.Command {
	const name = "list"
	return &flaggedVerb{
		name:     name,
		synopsis: "List the engine catalog (engines, registered versions, knob schema, capabilities)",
		usage:    "engines list",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.DataEngines.ListDataEngines(ctx, &structurev1.ListDataEnginesRequest{})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					for _, e := range resp.GetEngines() {
						versions := make([]string, 0, len(e.GetVersions()))
						for _, v := range e.GetVersions() {
							tag := v.GetId()
							if v.GetDefault() {
								tag += "*"
							}
							if v.GetDeprecated() {
								tag += " (deprecated"
								if v.GetNote() != "" {
									tag += ": " + v.GetNote()
								}
								tag += ")"
							}
							versions = append(versions, tag)
						}
						_, _ = fmt.Fprintf(env.Stdout, "%s %s (family %s, dialect %s, port %d)\n  versions: %s\n",
							e.GetEngine(), joinCapabilities(e.GetCapabilities()), e.GetFamily(), e.GetDialect(), e.GetPort(),
							strings.Join(versions, ", "))
						for _, k := range e.GetKnobs() {
							_, _ = fmt.Fprintf(env.Stdout, "  knob %s: kind %s, class %s, category %s, default %s\n",
								k.GetKey(), k.GetKind(), k.GetClass(), k.GetCategory(), k.GetDefaultValue())
						}
						if e.GetLicenseNote() != "" {
							_, _ = fmt.Fprintf(env.Stdout, "  license: %s\n", e.GetLicenseNote())
						}
					}
				})
			})
		},
	}
}

// joinCapabilities 把能力位压为人类短形（backup/migrate/browse/restore ——
// 无 = none）。
func joinCapabilities(caps *structurev1.EngineCapabilities) string {
	if caps == nil {
		return "[none]"
	}
	var parts []string
	if caps.GetBackup() {
		parts = append(parts, "backup")
	}
	if caps.GetMigrate() {
		parts = append(parts, "migrate")
	}
	if caps.GetBrowse() {
		parts = append(parts, "browse")
	}
	if caps.GetRestoreCompat() {
		parts = append(parts, "restore")
	}
	if len(parts) == 0 {
		return "[none]"
	}
	return "[" + strings.Join(parts, "/") + "]"
}
