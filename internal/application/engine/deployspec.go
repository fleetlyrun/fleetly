package engine

// 部署受理咽喉（P3 批 7：行为自 internal/interfaces 的冻结位 + hook 合并
// 块下沉，第 3 步起 api Deploy 与 webhook/refresh 链已全部切换本咽喉）：
// 合并（方向参数化）→ 三级变量合成 → Revision 冻结（内容寻址复用）→
// admission。四源归一化/旗标互斥/hook 闸门等请求形状执法留在 api——本域
// 只执合成/冻结/admission（engine 零 apperr：哨兵错误 + FrozenSpecError，
// errcode 映射住 api 层）。
//
// 序列化铁律：冻结体 marshal 与 api 层 marshalSpec 同参（protojson
// EmitUnpopulated:false + UseProtoNames:true）——本参数决定 Revision
// digest 的内容寻址；跨切换（api 改走本咽喉）参数漂移 = 同内容复用失配
//（冻结新行而非复用），只在可承受一次性数据迁移的批次变更。

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
)

// MergeMode 是冻结前与当前冻结体的合并方向（ADR-0072 所有权分裂）。
type MergeMode uint8

const (
	// MergeNone 直投不合并（非 hook-owned 或 override_hook 全量接管）。
	MergeNone MergeMode = iota
	// MergeMaterial push/refresh：新材料 + 冻结运行时——MergeHookOwned(提交件, 冻结体)。
	MergeMaterial
	// MergeOverlay spec_file overlay：冻结材料 + 提交件运行时（提交件
	// source 忽略——防旧 ref 构建旧 commit）——MergeHookOwned(冻结体, 提交件)。
	MergeOverlay
)

// SubmitSpecRequest 是规格受理请求（四源归一化/旗标互斥/hook 闸门留在
// api——请求形状执法 + engine 零 apperr；本域执合成/冻结/admission）。
type SubmitSpecRequest struct {
	// AppID 是目标 App 行 ID。
	AppID string
	// Spec 是 api 归一化产物（未合成平台变量）。
	Spec *specv1.AppSpec
	// SourceText 是冻结原文（compose yaml；其余形态空）。
	SourceText string
	// HookOwned 是 api 闸门判定产物（Merge != MergeNone 时必须 true——防御断言）。
	HookOwned bool
	// Merge 是冻结前与当前冻结体的合并方向。
	Merge MergeMode
	// Submit 非 nil = 冻结后走 admission；nil = 只冻结不部署。
	Submit *SubmitOptions
	// Confirmation 是 B1 确认票的受理入参透传（设计稿 §1.2：confirmation
	// 是 SubmitSpec 的入参；§1.4 甲'：④⑤内容绑定与烧票在 Submit 的
	// appMu 临界区内——冻结体产出之后）。只在 Submit != nil 时消费；
	// 只冻结形态携带 = 编程错误（预览管线铸票在 W2a，不占本入参）。
	Confirmation *ConfirmInput
}

// SubmitOptions 是 admission 透传面（幂等键/commit 去重锚/显式抢占）。
type SubmitOptions struct {
	IdempotencyKey string
	CommitSHA      string
	Supersede      bool
}

// FrozenSpecError 是冻结体损坏（protojson 反序列化失败）的类型化错误：
// 文案逐字节保持 api 原形态（"<site>: frozen revision is not valid
// protojson (<err>)"，site = "hook merge" | "hook-owned overlay"）——api
// 层映射 E_INTERNAL 时以 err.Error() 原文上信封（不变式⑦）。
type FrozenSpecError struct {
	Site string
	Err  error
}

func (e *FrozenSpecError) Error() string {
	return fmt.Sprintf("%s: frozen revision is not valid protojson (%v)", e.Site, e.Err)
}

func (e *FrozenSpecError) Unwrap() error { return e.Err }

// AdmissionError 标记 SubmitSpec 的 admission 相错误（不变式⑥双事务的映
// 射孪面）：api 按 phase 域插值——冻结相 what="revision"、admission 相
// what="deployment"（mapStateError 的 NotFound/Conflict 文案槽），文案
// 逐字节零漂移；哨兵判定经 Unwrap 照常穿透。
type AdmissionError struct {
	Err error
}

func (e *AdmissionError) Error() string { return e.Err.Error() }

func (e *AdmissionError) Unwrap() error { return e.Err }

// frozenSpecSite 是 FrozenSpecError 的站点词（api 原文案前缀逐字节保持：
// overlay = Deploy(spec_file) 合并位；material = webhook push 合并位）。
func frozenSpecSite(m MergeMode) string {
	if m == MergeOverlay {
		return "hook-owned overlay"
	}
	return "hook merge"
}

// errContentReused 是 Revision 冻结受理命中内容寻址复用的哨兵（事务内
// 错误只触发回滚——复用路径零写零事实；对外形态由 SubmitSpec 映射为复用
// 成功，errDeleteAborted 同款模式）。
var errContentReused = errors.New("revision: content-addressed reuse (identical spec already frozen)")

// SubmitSpec 是部署受理的唯一咽喉：合并（方向参数化）→ 三级变量合成 →
// Revision 冻结（内容寻址复用）→ admission（独立事务——freeze Tx 与
// Submit Tx 禁止并单：queue-full 时 Revision 保留可复用是行为）。返回值
// 依次为冻结行、受理部署行与判定附注；只冻结形态（Submit == nil）后两件
// 返回 nil。
func (e *Engine) SubmitSpec(ctx context.Context, req SubmitSpecRequest) (*revision.Revision, *deployment.Deployment, *Admission, error) {
	// 1. 防御断言（api 受理位已前置执法——这里是编程错误防线，非用户面
	// 校验：过此即调用方装配错误）。
	if req.AppID == "" || req.Spec == nil {
		return nil, nil, nil, fmt.Errorf("engine: submit spec: app_id and spec are required")
	}
	if req.Merge != MergeNone && !req.HookOwned {
		return nil, nil, nil, fmt.Errorf("engine: submit spec: merge without hook ownership is a programming error (the api gate must set HookOwned for MergeMaterial/MergeOverlay)")
	}
	if req.Confirmation != nil && req.Submit == nil {
		return nil, nil, nil, fmt.Errorf("engine: submit spec: confirmation without admission is a programming error (freeze-only intake never consumes tickets)")
	}
	// 3.5 带票受理的外来冻结体先判（修复批 F4：queue-full 重试 × webhook
	// 竞态的辨因序）：合成**之前**比对当前最新冻结体 digest 与票面锚——
	// 外来新冻结体（webhook 在预览/queue-full 拒绝与重试之间落的新 rev）
	// 即先判 STALE。不先判的误归因路径：merge 路径（MergeOverlay/
	// MergeMaterial）合成会并走外来新材料，产出的冻结体成为最新（恰为
	// self），⑤最新冻结体锚被 self 形态掩蔽放行，④ digest 必不符 →
	// MISMATCH——世界移动被误报成请求被改。self 判定 = 最新冻结体按
	// ConfirmRequestDigest 同参与票面指纹全等（本票确认的载荷自身：首次
	// 带票 Deploy 的冻结 / queue-full 重试的内容寻址复用）。
	if req.Confirmation != nil {
		if cerr := e.checkConfirmForeignFrozen(ctx, req); cerr != nil {
			return nil, nil, nil, cerr
		}
	}
	// 段二（merge + 三级变量合成）自本咽喉抽取为 composeSpec（B1 预览
	// 管线单源，设计稿 §1.3——Deploy 与 PreviewDeploy 同一合成函数产出；
	// 预览侧到此为止**不冻结**）。App 行载入（Project 轴解析）在
	// composeSpec 内完成。
	composed, _, err := e.composeSpec(ctx, req)
	if err != nil {
		return nil, nil, nil, err
	}
	req.Spec = composed

	// 4. 冻结（不变式⑨ 经 writes kernel 单源）：marshal 与 api 层
	// marshalSpec 同参（digest 关键——文件头铁律）；内容寻址复用命中即
	// 哨兵回滚（复用零写零事实，不变式①④）；facts 的双真偏离注记随迁
	//（见声明处）。
	//
	// 性能阈值锚(P5-5)：冻结 blob 分析容量 ≈3k task/冻结 Spec（分析容量
	// 锚，评审容量结论非实测上限）。形态：blob 整体单行物化，读回与
	// diff 均为全量操作，代价随单 Spec 进程数线性走。重估触发：单 Spec
	// 进程数常态逼近 3k 量级。
	body, merr := protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: true}.Marshal(req.Spec)
	if merr != nil {
		return nil, nil, nil, merr
	}
	var rev *revision.Revision
	ferr := e.tx.Do(ctx, func(s transact.Stores) error {
		return e.commitWrite(ctx, s, writeFact{
			checks: []func(ctx context.Context, s transact.Stores) error{func(ctx context.Context, s transact.Stores) error {
				existing, derr := s.Revisions.FindByDigest(ctx, req.AppID, revision.Digest(body))
				if derr == nil {
					rev = existing
					return errContentReused // 复用即受理终态：零冻结零事实
				}
				if !errors.Is(derr, state.ErrNotFound) {
					return derr
				}
				return nil
			}},
			write: func(ctx context.Context, s transact.Stores) error {
				seq, serr := s.Revisions.NextSeq(ctx, req.AppID)
				if serr != nil {
					return serr
				}
				rev = &revision.Revision{AppID: req.AppID, Seq: seq, Spec: body, SourceText: req.SourceText}
				return s.Revisions.Create(ctx, rev)
			},
			facts: []func() writes.FactRow{
				// 双真偏离（仅取证面）：Revision 冻结无流面孪生——材料面不是
				// 状态机，部署/构建的迁移事件由 engine 沿发射（A 化前亦只此
				// 一条审计行，行集合零漂移）。审计 actor/source 取 ctx
				//（webhook 路径经 WithAuditOverride 先于冻结注入，时序语义
				// 由 ctx 承载，不变式④）。
				func() writes.FactRow {
					return writes.FactRow{
						Action: "revision.create", Resource: "revision/" + rev.ID, AfterFP: rev.Digest,
						Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
						LedgerOnly: true,
					}
				},
			},
		})
	})
	if ferr != nil && !errors.Is(ferr, errContentReused) {
		return nil, nil, nil, ferr
	}
	// 内容寻址复用（ferr = 哨兵）：rev 已在 checks 内置行（既有冻结体）——
	// 同内容只冻结一份、不落新审计。

	// 5. admission（独立调用，不并事务——不变式⑥：admission 自己开 Tx；
	// queue-full 时冻结 Revision 保留可复用是行为）。
	if req.Submit == nil {
		return rev, nil, nil, nil // 只冻结不部署
	}
	// B1（设计稿 §1.4 甲'）：confirmation 透传 admission——④比对锚 =
	// 本临界区产出的冻结体 rev.Spec（marshalSpec 规范形；内容寻址复用
	// 命中的既有冻结体同 digest，④照常成立），比对与烧票都在 Submit 的
	// appMu 内闭合。
	d, adm, serr := e.Submit(ctx, SubmitRequest{
		AppID:           req.AppID,
		RevisionID:      rev.ID,
		IdempotencyKey:  req.Submit.IdempotencyKey,
		CommitSHA:       req.Submit.CommitSHA,
		Supersede:       req.Submit.Supersede,
		Confirm:         req.Confirmation,
		ConfirmSpecBody: rev.Spec,
	})
	if serr != nil {
		return nil, nil, nil, &AdmissionError{Err: serr}
	}
	return rev, d, adm, nil
}

// composeSpec 是 SubmitSpec 的段二抽取（B1 预览管线三段单源之一，设计稿
// §1.3）：App 行载入 → 合并（MergeNone/MergeMaterial/MergeOverlay，方向
// 参数化）→ 三级变量合成（Shared 底 < 源声明 env 中 < AppVariable 顶，
// 不变式②）。产出**合成后 Spec（不冻结）**——Deploy 受理咽喉与 PreviewSpec
// （预览管线）走同一函数，预览侧到此为止（冻结/admission/材料注入全不
// 触碰）。返回的 appRow 供调用方复用（Project 轴已解析）。
func (e *Engine) composeSpec(ctx context.Context, req SubmitSpecRequest) (*specv1.AppSpec, *app.App, error) {
	// App 行载入（Project 轴解析——SharedVariable 合成的装载锚）；行缺失/
	// 存储故障原样上抛，api 层 mapStateError 兜 errcode。
	appRow, err := e.repos.Apps.Get(ctx, req.AppID)
	if err != nil {
		return nil, nil, err
	}
	out := req.Spec

	// 合并（方向参数化，ADR-0072）：命中当前冻结体按方向合并；无冻结
	// Revision（hook 配置后从未部署）= 首部署形态，提交件原样受理——材料
	// 自提交件建立，后续 push 按合并语义接力。
	switch req.Merge {
	case MergeNone:
		// 直投不合并。
	case MergeMaterial, MergeOverlay:
		latest, lerr := e.repos.Revisions.Latest(ctx, req.AppID)
		if lerr == nil {
			frozen := &specv1.AppSpec{}
			if uerr := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(latest.Spec, frozen); uerr != nil {
				return nil, nil, &FrozenSpecError{Site: frozenSpecSite(req.Merge), Err: uerr}
			}
			// 参数序即方向（MergeHookOwned(material, runtime)——不变式③）：
			// overlay = 冻结材料 + 提交件运行时；push = 提交件材料 + 冻结运行时。
			if req.Merge == MergeOverlay {
				out = spec.MergeHookOwned(frozen, out)
			} else {
				out = spec.MergeHookOwned(out, frozen)
			}
		} else if !errors.Is(lerr, state.ErrNotFound) {
			return nil, nil, lerr
		}
	default:
		return nil, nil, fmt.Errorf("engine: submit spec: unknown merge mode %d", req.Merge)
	}

	// 三级变量合成（自 api 层冻结位移植；顺序即合成律——Shared 底 <
	// 源声明 env 中 < AppVariable 顶，不变式②）。装载失败上抛不吞
	//（P0-7.1 fail-closed：DB 瞬断不得静默吞成空环境）。
	if vars, verr := e.repos.SharedVariables.ListActive(ctx, appRow.ProjectID); verr != nil {
		return nil, nil, verr
	} else if len(vars) > 0 {
		shared := make(map[string]string, len(vars))
		for _, v := range vars {
			shared[v.Name] = v.Value
		}
		spec.MergeSharedEnv(out, shared)
	}
	if vars, verr := e.repos.AppVariables.ListActive(ctx, req.AppID); verr != nil {
		return nil, nil, verr
	} else if len(vars) > 0 {
		appVars := make(map[string]string, len(vars))
		for _, v := range vars {
			appVars[v.Name] = v.Value
		}
		spec.MergeAppEnv(out, appVars)
	}
	return out, appRow, nil
}
