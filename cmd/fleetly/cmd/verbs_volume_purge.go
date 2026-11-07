package cmd

// volumes purge 动词（B7，ADR-0094 决策 5 / design §2.4 升级正典③）：一次性
// 数据销毁——物理卷删除（容量回收）+ 行 tombstone；wide 档确认三形态
// （铸票面 = PreviewPurgeVolume）。自 verbs_structure.go 拆分（sizeguard
// 文件预算）。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynxland/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// volumes purge（B7，ADR-0094 决策 5 升级正典③/design §2.4）：一次性数据
// 销毁动作——物理卷删除（容量回收）+ 行 tombstone。wide 档确认三形态
// （铸票面 = PreviewPurgeVolume）：交互预览 y/N / --preview→--ticket 两调用
// 流 / --yes。预览披露：purge 清单（卷名/载体键/pinned 节点/关联库或独立
// 卷）+ 容量行（Runtime live 可得则携带）+「数据不可恢复」后果段
// （ADR-0086 wide 档：Consequences 先行）。
func newVolumesPurgeVerb() commands.Command {
	const name = "purge"
	var project string
	conf := confirmFlags{previewFace: true}
	return &flaggedVerb{
		name:     name,
		synopsis: "Purge a volume: destroy the runtime-side physical volume and tombstone the row (unrecoverable; wide-tier confirmation)",
		usage:    "volumes purge [--project PROJECT] VOLUME_ID [--yes | --preview | --ticket T]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare volume name)")
			conf.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one VOLUME_ID argument")
			}
			plan, err := conf.resolve("volumes purge")
			if err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				volumeID, err := resolveVolumeID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				req := &structurev1.PurgeVolumeRequest{Id: volumeID}
				// 确认前半：--preview 打印即退出；交互默认打印预演后 y/N
				//——y 以预演票带票直发，N 中止（零写请求）。
				if plan.mode == confirmPreviewOnly || plan.mode == confirmInteractive {
					preview, perr := c.Volumes.PreviewPurgeVolume(ctx, &structurev1.PreviewPurgeVolumeRequest{Id: volumeID})
					if perr != nil {
						return perr
					}
					if rerr := renderPurgePreview(env, jsonOut, preview); rerr != nil {
						return rerr
					}
					if plan.mode == confirmPreviewOnly {
						return nil
					}
					confirmed, cerr := promptConfirm(env, "Purge volume (data will be destroyed)")
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
				resp, err := c.Volumes.PurgeVolume(ctx, req)
				if err != nil {
					return presentConfirmError(err)
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintf(env.Stdout,
						"purged volume %s (carrier key %s)\nthe runtime-side physical volume and all data in it were destroyed; the platform row is tombstoned\n",
						resp.GetName(), resp.GetCarrierKey())
					if resp.GetUsageKnown() {
						_, _ = fmt.Fprintf(env.Stdout, "capacity reclaimed: %d bytes\n", resp.GetPurgedBytes())
					}
				})
			})
		},
	}
}

// resolveVolumeID 解析卷引用（id 直通 / --project + name——卷无 Resolve RPC，
// name 经 ListVolumes 项目内匹配；resolveDatabaseID 同形）。
func resolveVolumeID(ctx context.Context, c *fleetly.Client, projectFlag, ref string) (string, error) {
	if mintedIDRe.MatchString(ref) {
		return ref, nil
	}
	if projectFlag == "" {
		return "", fmt.Errorf("volume name %q requires --project (or pass the volume id)", ref)
	}
	projectID, err := resolveProjectID(ctx, c, projectFlag)
	if err != nil {
		return "", err
	}
	list, err := c.Volumes.ListVolumes(ctx, &structurev1.ListVolumesRequest{ProjectId: projectID})
	if err != nil {
		return "", err
	}
	for _, v := range list.GetVolumes() {
		if v.GetName() == ref {
			return v.GetId(), nil
		}
	}
	return "", fmt.Errorf("volume %q not found in project %s", ref, projectFlag)
}

// renderPurgePreview 渲染 purge 预演载荷（wide 档披露面，render 单点约定）：
// purge 清单（卷名/载体键/pinned 节点/关联库或独立卷）+ 容量行 + 后果段
// （「数据不可恢复」先行——ADR-0086 wide 档 Consequences 段）；ticket 行
// 殿后。机器形态 = PreviewPurgeVolumeResponse protojson。
func renderPurgePreview(env *commands.Environment, jsonOut bool, resp *structurev1.PreviewPurgeVolumeResponse) error {
	if jsonOut {
		return writeProtoJSON(env.Stdout, resp)
	}
	lines := []string{
		"purge preview for volume " + resp.GetName(),
		"  carrier key: " + resp.GetCarrierKey(),
	}
	if node := resp.GetPinnedNodeId(); node != "" {
		lines = append(lines, "  pinned node: "+node)
	} else {
		lines = append(lines, "  pinned node: (none)")
	}
	if resp.GetDatabaseId() != "" {
		lines = append(lines, fmt.Sprintf("  origin: data volume of database %s (id %s, now deleted)", resp.GetDatabaseName(), resp.GetDatabaseId()))
	} else {
		lines = append(lines, "  origin: standalone volume (no database association)")
	}
	if resp.GetUsageKnown() {
		lines = append(lines, fmt.Sprintf("  capacity in use: %d bytes (live from the runtime)", resp.GetUsageBytes()))
	} else {
		lines = append(lines, "  capacity in use: unknown (the runtime cannot report usage for this carrier)")
	}
	lines = append(lines,
		"consequences:",
		"  the runtime-side physical volume and ALL data in it will be destroyed",
		"  this cannot be undone; a same-named volume later starts empty")
	lines = append(lines, fmt.Sprintf("ticket: %s (expires in %ds)", resp.GetTicket(), resp.GetTicketExpiresIn()))
	_, err := fmt.Fprintln(env.Stdout, strings.Join(lines, "\n"))
	return err
}
