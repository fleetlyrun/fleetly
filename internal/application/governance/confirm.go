package governance

// Confirm Ticket 强制确认执法面（B1 变更确认闭环，设计稿 2026-10-30
// §1.2 重裁 §8.2' + §1.4 甲'方案；ADR 编号随 W5 补录）：默认强制 + 逃生门
// ——ConfirmableVerbs 表内动词无票且无 skip 即拒 E_CONFIRMATION_REQUIRED。
//
// 两段分工（设计稿 §1.4 验票序）：本拦截器承担通用半边（①票存在未过期
// →②purpose=confirm →③Verb 匹配 →请求者身份比对），Deploy/Rollback 族
// 的内容绑定（④request_digest vs 受理临界区冻结体、⑤base 多锚 vs 当前行）
// 与烧票下沉 engine 的 appMu 受理临界区（甲'裁决：handler 侧验票迫使合成
// 双跑、两步原语有 TOCTOU 同票双执行——一次下沉关闭两类洞）。
//
// 链序（assembly.NewInterceptors）：freeze → idem → confirm → limiter →
// timeout → handler。idem 命中重放先于 confirm——重放不验票不烧票
//（幂等降级窗内同键重跑撞已烧票 = 预期 E_CONFIRMATION_MISMATCH，设计稿
// §1.4 幂等降级窗）。豁免族不经本面：webhook/git 面（材料归 git
// ADR-0072，不经 Deploy 动词）、Restart/Scale（轻且可逆）、--yes（skip
// 显式通道，见下）。
//
// 票面与票据店：进程内票据店（fleetlygrpc.eventTicketStore）的第四
// purpose "confirm"；票面 payload 结构化（ConfirmTicketPayload）。本包
// 只定义消费端口（ConfirmTicketStore）与票面类型——铸造原语
// （issueConfirm）住票据店，PreviewDeploy/PreviewTeardown 的铸票调用归
// W2a。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
)

// ConfirmableVerbs 是强制确认集（表驱动立法，freezeguard 同型；设计稿
// §1.2 重裁：强制集 = Deploy + delete 族 + Rollback）。键 = FullMethod，
// 值 = 立法理由。confirmguard 守卫以 proto 为源双向对账：带 confirmation
// 字段的 RPC ⊆ 本表；本表内 RPC 的请求必有 confirmation 字段；
// PreviewDeploy/PreviewTeardown/ScaleProcess 豁免族不得入表。
var ConfirmableVerbs = map[string]string{
	"/fleetly.delivery.v1.DeploymentsService/Deploy": "four-source deploy admits a new Revision + Deployment (B1 mandatory set, design 2026-10-30 §1.2)",
	// Rollback 是单目标验票：票绑目标 id + 当前 revision digest + 操作者
	//（B2a 修护动词确认闭环的依赖）。
	"/fleetly.delivery.v1.DeploymentsService/Rollback": "replay to an old revision admits a new Deployment (B1 mandatory set, single-target ticket, design 2026-10-30 §1.2)",
	// DeleteApp 属 delete 族（拆除不可逆，级联清单必须被确认过）；
	// Restart/Scale 轻且可逆不入表（豁免族，设计稿 §1.2）。
	"/fleetly.structure.v1.AppsService/DeleteApp": "irreversible teardown with a cascading row purge (B1 delete family, design 2026-10-30 §1.2)",
}

// confirmField 是请求携带确认票面的字段名（三请求共用 Contract 单源：
// DeployRequest/RollbackRequest/DeleteAppRequest 的 optional Confirmation）。
const confirmField = "confirmation"

// ConfirmSemantics 是入指纹的提交语义位（设计稿 §1.4 票面绑定物：同一
// spec 不同提交语义的用户可见后果分叉，独立审查轮 #3）。Supersede = 显式
// 抢占在途；OverrideHook = hook 所有权一次性接管（ADR-0072）。
type ConfirmSemantics struct {
	Supersede    bool
	OverrideHook bool
}

// String 输出指纹输入的规范形（键序固定，确定性序列化纪律）。
func (s ConfirmSemantics) String() string {
	return fmt.Sprintf("supersede=%t;override_hook=%t", s.Supersede, s.OverrideHook)
}

// ConfirmRequestDigest 计算票面 request_digest 的单源指纹（设计稿 §1.4）：
// sha256(marshalSpec 规范形 specBody + 提交语义位)——铸票面（W2a 预览
// 管线）与消费面（engine 受理临界区）同参数调用，漂移即 MISMATCH。
func ConfirmRequestDigest(specBody []byte, sem ConfirmSemantics) string {
	h := sha256.New()
	h.Write(specBody)
	h.Write([]byte("\n" + sem.String()))
	return hex.EncodeToString(h.Sum(nil))
}

// ConfirmVarEnvDigest 计算活跃变量环境摘要（base 多锚之一，设计稿 §1.4
// #7：服务端合成输入漂移——变量行变化——由此归 STALE）：输入是合成后
// 环境（Shared 底 + App 顶、App 胜出，与 SubmitSpec 三级合成同向），
// 输出 = sorted "name=value" 行的 sha256（确定性序列化纪律）。铸票面
// 与 engine 消费面同参数调用。
func ConfirmVarEnvDigest(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(name + "=" + env[name] + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ConfirmRouteSetDigest 计算 route 行 ID 集摘要（teardown 票 base 锚之一，
// 设计稿 §1.4 #8：预览后新增的 route 若不锚，确认时会被清单从未展示地
// 删除）：输入是目标 App 名下的 route 行 ID 集，输出 = sorted ID 行的
// sha256；空集归空串（与 ConfirmVarEnvDigest 的缺席锚形态一致）。铸票面
// （PreviewTeardown）与 engine 消费面（peekTeardownConfirm）同参数调用。
func ConfirmRouteSetDigest(routeIDs []string) string {
	if len(routeIDs) == 0 {
		return ""
	}
	ids := append([]string(nil), routeIDs...)
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ConfirmBase 是基线多锚（设计稿 §1.4：服务端合成输入漂移由此归 STALE
// 而非误判 MISMATCH，独立审查轮 #7）：最新冻结体 digest、最近成功部署锚
// digest、活跃变量环境摘要、hook 行版本。四锚在 engine 受理临界区与当前
// 行比对（⑤）。
//
// AppRowVersion 与 RouteSetDigest 是 teardown 票的专用锚（设计稿 §1.4
// teardown 票绑定：base = app 行版本 + hook 行 digest + route 行 ID 集摘要
// ——W2a 补齐）：Deploy 票恒空，checkConfirmBase（Deploy 消费面）不比对；
// peekTeardownConfirm（teardown 消费面）比对。锚在票面为空 = 铸票时该
// 对象缺席（无 hook 行/无 route 行），消费面仍无条件全等比对（空==空
// 闭合，空↔非空即 STALE——修复批 F2，checkConfirmBase 空串全等同形态）。
type ConfirmBase struct {
	LatestFrozenDigest    string
	LatestSucceededDigest string
	VarEnvDigest          string
	HookRowVersion        string
	AppRowVersion         string
	RouteSetDigest        string
}

// ConfirmOperator 是票面操作者烙印（设计稿 §1.4 ③：票不得跨操作者——
// events 票据的身份解绑理由〔匿名 SSE 无身份〕不迁移到已认证面）。
type ConfirmOperator struct {
	TokenID string
	UserID  string
	TeamID  string
}

// ConfirmTicketPayload 是 confirm 票面绑定物（结构化承载，设计稿 §1.4）：
// Verb/TargetApp 绑定兑换动词与目标；RequestDigest（含 Semantics）绑定
// 确认过的载荷；Base 多锚绑定预览时刻的世界；PreviewDigest 绑定预览
// 载荷本体；Operator 烙印操作者。
type ConfirmTicketPayload struct {
	Verb          string
	TargetApp     string
	RequestDigest string
	Semantics     ConfirmSemantics
	Base          ConfirmBase
	PreviewDigest string
	Operator      ConfirmOperator
}

// ConfirmPeekStatus 是 peekConfirm 的辨因结果（tombstone 窗使「已确认过」
// 与「过期/伪造」可分辨——设计稿 §1.4 烧票契约 #4）。
type ConfirmPeekStatus int

const (
	// ConfirmPeekHit：票在册、未过期、purpose=confirm。
	ConfirmPeekHit ConfirmPeekStatus = iota
	// ConfirmPeekExpired：票在册但已过 TTL（时间维度不新鲜 → STALE 腿）。
	ConfirmPeekExpired
	// ConfirmPeekUnknown：票不存在或 purpose 不符（伪造/错面票 →
	// MISMATCH 腿；与 Expired 的互辨不是票据店承诺——沿「不区分理由」
	// 纪律，与 Consumed 的互辨才是 tombstone 的目的）。
	ConfirmPeekUnknown
	// ConfirmPeekConsumed：票已烧（tombstone 短窗内）——已确认过，与
	// 过期/伪造不同错误信息（并发同票第二请求/幂等降级窗的预期腿）。
	ConfirmPeekConsumed
)

// ConfirmTicketStore 是 confirm 票据面的消费端口（进程内票据店实现，
// assembly 注入拦截器与 engine；peek 不删、discard 单用途烧 + tombstone
// 短窗）。engine 侧内容绑定与烧票都在 appMu 受理临界区内调用本面。
type ConfirmTicketStore interface {
	// PeekConfirm 验票不删：命中返回票面与 ConfirmPeekHit。
	PeekConfirm(ticket string) (ConfirmTicketPayload, ConfirmPeekStatus)
	// DiscardConfirm 烧票：命中未过期 confirm 票即删并落 tombstone；
	// 返回是否真烧（不存在/已烧/已过期 = false，不重复落 tombstone）。
	DiscardConfirm(ticket string) bool
}

// ConfirmGuard 是强制确认拦截器（一份实现覆盖 ConfirmableVerbs 全部
// 动词；通用验票四步，内容绑定半边在 engine）。
type ConfirmGuard struct {
	tickets ConfirmTicketStore
}

// NewConfirmGuard 构造拦截器（票据面由组合根注入——单实例票据店与
// engine 侧消费共享同一 store）。
func NewConfirmGuard(tickets ConfirmTicketStore) *ConfirmGuard {
	return &ConfirmGuard{tickets: tickets}
}

// Unary 返回拦截器：非强制集原样放行（豁免族天然不经过本面）。
func (g *ConfirmGuard) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if _, ok := ConfirmableVerbs[info.FullMethod]; !ok {
			return handler(ctx, req)
		}
		if err := g.enforce(ctx, req, info.FullMethod); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// enforce 执行通用验票（①②③ + 请求者身份比对；④⑤内容绑定与烧票在
// engine 受理临界区）：
//
//	skip=true            → 直接放行（--yes 逃生门；审计 proof=skipped 随
//	                        engine 受理落 LedgerOnly 行，取证面可辨「确认
//	                        过」「跳过」「无凭证拒绝」三形态）；
//	无票且无 skip        → E_CONFIRMATION_REQUIRED；
//	①票不存在/purpose 不符 → E_CONFIRMATION_MISMATCH（伪造腿）；
//	①票已过期           → E_CONFIRMATION_STALE（时间维度不新鲜，重预览）；
//	①票已烧（tombstone）  → E_CONFIRMATION_MISMATCH（已确认过腿）；
//	③Verb 不匹配         → E_CONFIRMATION_MISMATCH；
//	③请求者身份不符       → E_CONFIRMATION_MISMATCH（票不得跨操作者）。
func (g *ConfirmGuard) enforce(ctx context.Context, req any, method string) error {
	ticket, skip := confirmationOf(req)
	if skip {
		return nil // --yes 显式跳过：进审计（engine 侧 proof=skipped），此处放行
	}
	if ticket == "" {
		return apperr.New("E_CONFIRMATION_REQUIRED",
			"%s requires confirmation: pass the ticket from a preview, or set skip to confirm without a preview (--yes)", method).
			WithSuggestion("Run the matching preview RPC to mint a one-shot ticket (600s TTL), or pass --yes for scripted calls.")
	}
	payload, status := g.tickets.PeekConfirm(ticket)
	switch status {
	case ConfirmPeekHit:
	case ConfirmPeekExpired:
		return staleErr(method)
	case ConfirmPeekConsumed:
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket was already redeemed; check the deployment state instead of resending the ticket").
			WithSuggestion("The ticket is single-use. Query the resulting deployment for the outcome; mint a fresh preview ticket for a new change.")
	case ConfirmPeekUnknown:
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket is not valid for this operation").
			WithSuggestion("Mint a fresh ticket from the matching preview RPC; tickets are single-purpose and bound to the previewing operator.")
	}
	if payload.Verb != method {
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket was minted for a different operation").
			WithSuggestion("Each ticket binds the verb that previewed it; mint a fresh preview for this operation.")
	}
	if err := matchOperator(ctx, payload.Operator); err != nil {
		return err
	}
	return nil
}

// staleErr 构造基线/时间漂移拒绝（suggestion 指向重预览）。
func staleErr(method string) error {
	return apperr.New("E_CONFIRMATION_STALE",
		"the confirmation for %s no longer matches the current state", method).
		WithSuggestion("The baseline moved (a newer revision, deployment, variable environment or hook row) or the ticket expired. Run the preview again and confirm the fresh ticket.")
}

// matchOperator 比对请求者身份与票面操作者（TokenID/UserID/TeamID 三锚
// 全等；无身份 = fail-closed 拒绝——SERVER 档由 authn 先行，此处纵深）。
func matchOperator(ctx context.Context, want ConfirmOperator) error {
	id, ok := authn.FromContext(ctx)
	if !ok {
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket cannot be verified without an authenticated caller").
			WithSuggestion("Tickets are bound to the operator that previewed them; send the request with the same token.")
	}
	if id.TokenID != want.TokenID || id.UserID != want.UserID || id.TeamID != want.TeamID {
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket belongs to a different operator").
			WithSuggestion("A ticket cannot be consumed by another operator; the previewing operator confirms, or a fresh preview is minted under this identity.")
	}
	return nil
}

// confirmationOf 经 protoreflect 读请求的 confirmation 字段（ticket 与
// skip；字段缺省形态 = 未携带）。非 proto 请求按未携带处理（受控面不可达
// 的纵深）。
func confirmationOf(req any) (ticket string, skip bool) {
	m, ok := req.(proto.Message)
	if !ok {
		return "", false
	}
	msg := m.ProtoReflect()
	fd := msg.Descriptor().Fields().ByName(confirmField)
	if fd == nil || fd.Kind() != protoreflect.MessageKind || !msg.Has(fd) {
		return "", false
	}
	conf := msg.Get(fd).Message()
	ticketField := conf.Descriptor().Fields().ByName("ticket")
	if ticketField != nil && ticketField.Kind() == protoreflect.StringKind {
		ticket = conf.Get(ticketField).String()
	}
	skipField := conf.Descriptor().Fields().ByName("skip")
	if skipField != nil && skipField.Kind() == protoreflect.BoolKind {
		skip = conf.Get(skipField).Bool()
	}
	return ticket, skip
}
