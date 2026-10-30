package cmd

// B1 W3：Confirm Ticket 的 CLI 三形态面（设计稿 2026-10-30 §1.6，ADR-0085
// 词汇冻结——票面词条是 ticket，不用外词形态）：
//
//	交互默认（TTY）    ：调 Preview RPC → 人类形态打印 → y/N 提示 → y 发
//	                     带票请求（「确认的正是展示的」在 CLI 侧的投影）；
//	--yes              ：显式 skip=true 直发（脚本/CI 逃生门；审计
//	                     proof=skipped）；
//	--preview          ：打印预览 + ticket 行后退出（两调用流第 1 步，
//	                     Agent 形态；--ticket 是第 2 步）；
//	非 TTY 且无旗标    ：不发请求，本地报错指引（机器环境没有可读的 y/N）。
//
// 铸票面的现状事实：PreviewDeploy/PreviewTeardown 已实装；Rollback 在
// ConfirmableVerbs 强制集内但**尚无预览铸票 RPC**（铸票面随 B2a 修护动词
// 批落地）——rollback 本批只有 --yes/--ticket 两形态，裸调用（无旗标）
// 本地诚实报错而不是交互（交互确认后无票可带，y 之后必然
// E_CONFIRMATION_REQUIRED）。
//
// 服务端三拒绝码的 CLI 呈现（presentConfirmError）：REQUIRED→指引；STALE
//→「state moved on since preview, re-run --preview」；MISMATCH→「ticket
// does not match this request/operator」（幂等降级窗内同键重跑撞已烧票
// = 预期 MISMATCH，设计稿 §1.4 诚实边界——文案同腿）。

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lynx-go/commands"
	"golang.org/x/term"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
)

// confirmFlags 是强制确认动词共享的旗标面（--yes/--preview/--ticket）。
// previewFace 标记动词是否有服务端预览铸票 RPC：false 时不注册 --preview
// （死旗标比缺席更糟），裸调用走无预览面的报错腿。
type confirmFlags struct {
	yes     bool
	preview bool
	ticket  string

	previewFace bool
}

// declare 注册确认旗标（旗标文案是 Agent 可发现面，英文逐字 golden 钉死）。
func (f *confirmFlags) declare(fs *flag.FlagSet) {
	fs.BoolVar(&f.yes, "yes", false,
		"skip the change preview confirmation and send directly (scripted/CI escape hatch; audited as skipped)")
	if f.previewFace {
		fs.BoolVar(&f.preview, "preview", false,
			"print the change preview and the confirmation ticket, then exit (first call of the two-call flow; confirm with --ticket)")
	}
	fs.StringVar(&f.ticket, "ticket", "",
		"send with a confirmation ticket minted by --preview (single-use, 600s TTL; pairs with --wait)")
}

// confirmMode 是三形态解析结果（无旗标 = 交互默认）。
type confirmMode int

const (
	confirmInteractive confirmMode = iota // TTY：预览 → y/N → 带票直发
	confirmSkip                           // --yes：skip=true 直发
	confirmPreviewOnly                    // --preview：打印预览 + ticket 后退出
	confirmTicketed                       // --ticket T：带票直发
)

// confirmPlan 是一次执行请求的确认消费计划。
type confirmPlan struct {
	mode   confirmMode
	ticket string
}

// resolve 把旗标组合解析为确认计划：三旗标互斥（usage 错误，exit 64）；
// 无旗标时按预览面在场性与 TTY 状态分流（本地拒绝不发请求——非 TTY 环境
// 没有可读的 y/N，静默直发会撞服务端 REQUIRED 白付一次拨号）。
func (f *confirmFlags) resolve(verb string) (*confirmPlan, error) {
	set := 0
	for _, on := range []bool{f.yes, f.preview, f.ticket != ""} {
		if on {
			set++
		}
	}
	if set > 1 {
		return nil, usageErr(verb, "--yes, --preview and --ticket are mutually exclusive (pick one confirmation form)")
	}
	switch {
	case f.yes:
		return &confirmPlan{mode: confirmSkip}, nil
	case f.preview:
		return &confirmPlan{mode: confirmPreviewOnly}, nil
	case f.ticket != "":
		return &confirmPlan{mode: confirmTicketed, ticket: f.ticket}, nil
	}
	if !f.previewFace {
		// Rollback 腿：强制集成员但铸票面未落地（B2a 修护动词批补）。
		// 交互确认后无票可带，y 之后必然 REQUIRED——本地诚实拒绝。
		return nil, fmt.Errorf(
			"%s requires confirmation but no preview face exists for it yet: pass --yes to send without a preview (--ticket accepts tickets minted by the upcoming fix-verb faces)",
			verb)
	}
	if !confirmIsTTY() {
		return nil, fmt.Errorf(
			"%s requires confirmation and stdin is not a terminal: use the --preview/--ticket two-step, or --yes",
			verb)
	}
	return &confirmPlan{mode: confirmInteractive}, nil
}

// confirmation 铸执行请求的确认票面（交互形态的票来自预览响应，由调用方
// 覆写；PreviewOnly 不发执行请求，恒 nil）。
func (p *confirmPlan) confirmation() *deliveryv1.Confirmation {
	switch p.mode {
	case confirmSkip:
		return &deliveryv1.Confirmation{Skip: true}
	case confirmTicketed:
		return &deliveryv1.Confirmation{Ticket: p.ticket}
	}
	return nil
}

// confirmIsTTY 报告 stdin 是否交互终端（交互默认形态的准入条件）；测试经
// 本接缝伪 TTY（golden 钉交互形态，设计稿 §1.6「伪 TTY 或抽打印函数」）。
var confirmIsTTY = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// confirmReadAnswer 读一行确认应答（y/N）；测试经本接缝注入 canned 应答。
var confirmReadAnswer = func() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return line, nil
}

// promptConfirm 打印 y/N 提示并读取应答：y/yes 确认，其余（含空回车）拒绝
// ——N 是缺省安全侧。提示走 stdout（与预览块同面收束进交互输出；golden
// 逐字钉死），应答本身不回显。
func promptConfirm(env *commands.Environment, verb string) (bool, error) {
	if _, err := fmt.Fprintf(env.Stdout, "%s? [y/N] ", verb); err != nil {
		return false, err
	}
	line, err := confirmReadAnswer()
	if err != nil {
		return false, err
	}
	_, _ = fmt.Fprintln(env.Stdout) // 收束提示行（读取已吞应答回车）
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// confirmSkipNote 是 --yes 通道的一行说明（仅人类形态——--json 输出保持
// 纯 protojson 契约，通道事实由旗标本身承载）。
const confirmSkipNote = "skipped preview confirmation (--yes)"

// presentConfirmError 把服务端三拒绝码呈现为 CLI 指引（W3，设计稿 §1.6）：
// 保持码前缀可 grep、单行纯文本（不携 error_id——指引是确定性文案）。
// 其余错误原样上抛走错误信封。
func presentConfirmError(err error) error {
	env, ok := apperrstatus.FromError(err)
	if !ok {
		return err
	}
	switch env.Code() {
	case "E_CONFIRMATION_REQUIRED":
		return fmt.Errorf("E_CONFIRMATION_REQUIRED: this call needs confirmation: use the --preview/--ticket two-step, or --yes")
	case "E_CONFIRMATION_STALE":
		return fmt.Errorf("E_CONFIRMATION_STALE: state moved on since the preview; re-run --preview to mint a fresh ticket")
	case "E_CONFIRMATION_MISMATCH":
		return fmt.Errorf("E_CONFIRMATION_MISMATCH: ticket does not match this request/operator (unknown, already redeemed, or minted for a different request); re-run --preview and confirm the fresh ticket")
	}
	return err
}

// enumShort 剥枚举名的类型前缀取短形（WORKLOAD_ROLLOUT_REPLACE → replace；
// PREVIEW_WARNING_KIND_LIVE_DRIFT → live_drift——人类形态徽章/分类轴）。
func enumShort(name, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

// renderDeployPreview 渲染部署预览载荷（人类/机器双形态，render 单点约定）：
// spec diff 摘要 / workload changes 逐条带 rollout 徽章 / warnings 逐条；
// ticket 行殿后（--preview 与交互形态共面——「确认的正是展示的」的 CLI
// 投影，票即展示物的回执）。机器形态 = PreviewDeployResponse protojson
// （ticket 与 ticket_expires_in 字段 native 携带，无需附加行）。
func renderDeployPreview(env *commands.Environment, jsonOut bool, app string, resp *deliveryv1.PreviewDeployResponse) error {
	if jsonOut {
		return writeProtoJSON(env.Stdout, resp)
	}
	_, err := fmt.Fprintf(env.Stdout, "deploy preview for app %s\n", app)
	if err != nil {
		return err
	}
	if diff := resp.GetSpecDiff(); len(diff) == 0 {
		if _, err := fmt.Fprintln(env.Stdout, "spec diff: no changes"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(env.Stdout, "spec diff (%d entries):\n", len(diff)); err != nil {
			return err
		}
		for _, e := range diff {
			if _, err := fmt.Fprintf(env.Stdout, "  %s: %s -> %s\n", e.GetPath(), e.GetOldValue(), e.GetNewValue()); err != nil {
				return err
			}
		}
	}
	if changes := resp.GetWorkloadChanges(); len(changes) == 0 {
		if _, err := fmt.Fprintln(env.Stdout, "workload changes: none"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(env.Stdout, "workload changes (%d):\n", len(changes)); err != nil {
			return err
		}
		for _, ch := range changes {
			line := fmt.Sprintf("  %s %s image=%s replicas %d->%d rollout=%s",
				ch.GetWorkloadId(),
				enumShort(ch.GetAction().String(), "WORKLOAD_CHANGE_ACTION_"),
				ch.GetImage(), ch.GetReplicasFrom(), ch.GetReplicasTo(),
				enumShort(ch.GetRollout().String(), "WORKLOAD_ROLLOUT_"))
			if cmd := ch.GetCommand(); len(cmd) > 0 {
				line += fmt.Sprintf(" command=%q", strings.Join(cmd, " "))
			}
			if _, err := fmt.Fprintln(env.Stdout, line); err != nil {
				return err
			}
		}
	}
	if warnings := resp.GetWarnings(); len(warnings) == 0 {
		if _, err := fmt.Fprintln(env.Stdout, "warnings: none"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(env.Stdout, "warnings (%d):\n", len(warnings)); err != nil {
			return err
		}
		for _, w := range warnings {
			if _, err := fmt.Fprintf(env.Stdout, "  %s: %s\n",
				enumShort(w.GetKind().String(), "PREVIEW_WARNING_KIND_"), w.GetDetail()); err != nil {
				return err
			}
		}
	}
	_, err = fmt.Fprintf(env.Stdout, "ticket: %s (expires in %ds)\n", resp.GetTicket(), resp.GetTicketExpiresIn())
	return err
}

// renderTeardownPreview 渲染拆除预览载荷（apps delete 面）：级联清单（将拆
// 载体/将删 hook/route 行）+ 保留清单带理由 + 驱动权冲突透出；ticket 行
// 殿后（形态同 renderDeployPreview）。
func renderTeardownPreview(env *commands.Environment, jsonOut bool, app string, resp *deliveryv1.PreviewTeardownResponse) error {
	if jsonOut {
		return writeProtoJSON(env.Stdout, resp)
	}
	_, err := fmt.Fprintf(env.Stdout, "teardown preview for app %s\n", app)
	if err != nil {
		return err
	}
	if cascade := resp.GetCascade(); len(cascade) == 0 {
		if _, err := fmt.Fprintln(env.Stdout, "cascade: nothing to tear down"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(env.Stdout, "cascade (%d):\n", len(cascade)); err != nil {
			return err
		}
		for _, item := range cascade {
			if _, err := fmt.Fprintf(env.Stdout, "  %s %s\n",
				enumShort(item.GetKind().String(), "TEARDOWN_ITEM_KIND_"), item.GetName()); err != nil {
				return err
			}
		}
	}
	if kept := resp.GetKept(); len(kept) == 0 {
		if _, err := fmt.Fprintln(env.Stdout, "kept: nothing survives this teardown by reference"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(env.Stdout, "kept (%d):\n", len(kept)); err != nil {
			return err
		}
		for _, item := range kept {
			if _, err := fmt.Fprintf(env.Stdout, "  %s: %s\n",
				enumShort(item.GetKind().String(), "KEPT_ITEM_KIND_"), item.GetReason()); err != nil {
				return err
			}
		}
	}
	if resp.GetDrivingConflict() {
		if _, err := fmt.Fprintf(env.Stdout, "driving conflict: %s\n", resp.GetDrivingDetail()); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(env.Stdout, "driving conflict: none"); err != nil {
		return err
	}
	_, err = fmt.Fprintf(env.Stdout, "ticket: %s (expires in %ds)\n", resp.GetTicket(), resp.GetTicketExpiresIn())
	return err
}
