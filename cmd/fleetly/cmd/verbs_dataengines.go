package cmd

// engines 命令组（B2c，ADR-0094 决策 4 / ADR-0095 决策 10）：Engine Catalog
// 的 CLI 取值面——引擎/在册版本（默认与弃用）/旋钮 schema/能力，全部从
// 服务端目录 RPC 现场取值（硬编码引擎常量退役路径的读半边；Console 目录
// 驱动表单随 B3）。双形态 golden 钉死。B3 增 databases config describe
// （单引擎旋钮面人读表，同取值面）。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynxland/commands"
	"google.golang.org/protobuf/proto"

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
						_, _ = fmt.Fprintf(env.Stdout, "%s %s (kind %s, dialect %s, port %d)\n  versions: %s\n",
							e.GetEngine(), joinCapabilities(e.GetCapabilities()), e.GetKind(), e.GetDialect(), e.GetPort(),
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

// databases config describe（B3，ADR-0095 决策 10 / design §3.5）：Engine
// Catalog 单引擎的旋钮面人读表——键/kind/范围或 enum/缺省/class 徽标
// （instant = dynamic 运行时生效；rolling = static 受监督滚动）/分组。
// --json = 该引擎的目录行（protojson EngineCatalog，versions 收窄为所述
// 版本——旋钮 schema 的适用面；Agent 面完整 schema）。取值单源 = 服务端
// 目录（ListDataEngines），CLI 零引擎常量：引擎/版本未在册的报错列目录
// 现值。旋钮集各版本同集（模板注册表现状），--version 定位适用版本并回显
// 其 default/deprecated 标注。
func newDatabasesConfigDescribeVerb() commands.Command {
	const name = "describe"
	var version string
	return &flaggedVerb{
		name:     name,
		synopsis: "Describe an engine's knob schema from the engine catalog (kind, range, default, instant/rolling class)",
		usage:    "databases config describe [--version VERSION] ENGINE",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&version, "version", "", "registered version id to describe (empty = the engine default; list legal values with 'fleetly engines list')")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one ENGINE argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.DataEngines.ListDataEngines(ctx, &structurev1.ListDataEnginesRequest{})
				if err != nil {
					return err
				}
				entry, ver, err := catalogEntryFor(resp.GetEngines(), args[0], version)
				if err != nil {
					return err
				}
				described, _ := proto.Clone(entry).(*structurev1.EngineCatalog)
				described.Versions = []*structurev1.EngineVersionInfo{ver}
				return renderOut(env, jsonOut, described, func() { renderKnobSchema(env, described, ver) })
			})
		},
	}
}

// catalogEntryFor 在目录里定位引擎行与版本条目（version 空 = Default 条
// 目）。未在册即报错并列目录现值（值域单源 = 服务端目录）。
func catalogEntryFor(engines []*structurev1.EngineCatalog, engine, version string) (*structurev1.EngineCatalog, *structurev1.EngineVersionInfo, error) {
	var names []string
	for _, e := range engines {
		names = append(names, e.GetEngine())
		if e.GetEngine() != engine {
			continue
		}
		var ids []string
		for _, v := range e.GetVersions() {
			ids = append(ids, v.GetId())
			if (version == "" && v.GetDefault()) || (version != "" && v.GetId() == version) {
				return e, v, nil
			}
		}
		if version == "" {
			return nil, nil, fmt.Errorf("engine %s has no default version in the engine catalog (registered: %s)", engine, strings.Join(ids, ", "))
		}
		return nil, nil, fmt.Errorf("engine %s has no registered version %q (registered: %s)", engine, version, strings.Join(ids, ", "))
	}
	return nil, nil, fmt.Errorf("unknown engine %q (the engine catalog lists: %s)", engine, strings.Join(names, ", "))
}

// versionTag 渲染版本条目短形（default / deprecated + note 标注）。
func versionTag(v *structurev1.EngineVersionInfo) string {
	tag := v.GetId()
	switch {
	case v.GetDeprecated() && v.GetNote() != "":
		tag += " (deprecated: " + v.GetNote() + ")"
	case v.GetDeprecated():
		tag += " (deprecated)"
	case v.GetDefault():
		tag += " (default)"
	}
	return tag
}

// knobRange 渲染旋钮值域列：bool = true|false；enum = 值集；int/size/
// duration = min..max（空界省略，双空 = -）。
func knobRange(k *structurev1.EngineKnob) string {
	switch k.GetKind() {
	case "bool":
		return "true|false"
	case "enum":
		return strings.Join(k.GetEnumValues(), "|")
	}
	if k.GetMin() == "" && k.GetMax() == "" {
		return "-"
	}
	return k.GetMin() + ".." + k.GetMax()
}

// orDash 渲染空值单元格。
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// renderKnobSchema 渲染单引擎旋钮面人读表（制表符分列，list 动词同惯例）：
// 标题行带版本标注与端口；表后 class 图例 + 值形态提示（size/duration——
// 受理位 ValidateKnobValue 同口径）+ 设置入口。
func renderKnobSchema(env *commands.Environment, e *structurev1.EngineCatalog, v *structurev1.EngineVersionInfo) {
	lines := []string{fmt.Sprintf("%s knob schema: version %s, kind %s, port %d",
		e.GetEngine(), versionTag(v), e.GetKind(), e.GetPort())}
	knobs := e.GetKnobs()
	if len(knobs) == 0 {
		lines = append(lines, "no knobs registered (this engine exposes no settings yet)")
		_, _ = fmt.Fprintln(env.Stdout, strings.Join(lines, "\n"))
		return
	}
	lines = append(lines, "KEY\tKIND\tRANGE\tDEFAULT\tCLASS\tCATEGORY")
	kinds := map[string]bool{}
	for _, k := range knobs {
		kinds[k.GetKind()] = true
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s", k.GetKey(), k.GetKind(), knobRange(k),
			orDash(k.GetDefaultValue()), knobClassBadge(k.GetClass()), orDash(k.GetCategory())))
	}
	lines = append(lines, "class: instant = takes effect live without a restart (dynamic); rolling = supervised rolling restart of the carrier (static)")
	if kinds["size"] {
		lines = append(lines, "size values: <number><b|kb|mb|gb>, e.g. 512mb (units are case-insensitive)")
	}
	if kinds["duration"] {
		lines = append(lines, "duration values: <number><ms|s|m|h|d>, e.g. 30s")
	}
	lines = append(lines, "set knobs with --set KEY=VALUE on 'fleetly databases create' or 'fleetly databases update'")
	_, _ = fmt.Fprintln(env.Stdout, strings.Join(lines, "\n"))
}
