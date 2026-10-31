package fleetlygrpc

// B1 PreviewTeardown / ScaleProcess 的 api 实装（W2a，设计稿 §1.3
// Teardown 面 + §1.2 ScaleProcess 一等 RPC）。
//
// PreviewTeardown 是**新装配非单源**（设计稿 §1.3：行级联在 API 层
// tombstone 事务、保留是缺席语义——预演面按 DeleteApp 的行为只读镜像
// 组装）：级联清单（engine.TeardownCascade 铸/验共享单源——将拆载体
// 〔投影推导〕+ 将删 hook/route 行〔structure.go tombstone 事务的行为
// 只读预演〕）+ 保留清单带理由 + DrivingByApp 冲突透出（三层同谓词预检
// 的只读版）；铸票 base = app 行版本 + hook 行 digest + route 行 ID 集
// 摘要（§1.4，engine.TeardownAnchors 与 peekTeardownConfirm 消费面同源）。
//
// ScaleProcess 豁免确认（Restart/Scale 豁免族——不进 ConfirmableVerbs，
// W2b 已钉）：服务端展开为当前生效 Spec 的 scale-only overlay 提交。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
)

// PreviewTeardown 是 DeleteApp 的只读预演（B1 Teardown 面）：级联清单 +
// 保留清单 + 驱动权冲突 + 铸票。行级联镜像 DeleteApp 的 tombstone 事务
// 行为（App tombstone + Hooks.DeleteByApp + Routes 撤销）；保留清单是
// 缺席语义的显式透出（database/volume/secret/upload 四族按生效 Spec 的
// 引用集枚举——未被引用的族不进场，清单只回答「本 App 的拆除会碰到什么」）。
func (svc *DeploymentsService) PreviewTeardown(ctx context.Context, req *deliveryv1.PreviewTeardownRequest) (*deliveryv1.PreviewTeardownResponse, error) {
	// 行级授权（镜像 DeleteApp：载行 + 归属比对；scope apps:admin 由方法
	// policy 执法——预演动词不得低于被预演动词的权限门槛，设计稿 §1.2 A4）。
	appRow, err := svc.s.authorizeAppID(ctx, req.GetAppId())
	if err != nil {
		return nil, err
	}
	out := &deliveryv1.PreviewTeardownResponse{}

	// 驱动权冲突（三层同谓词预检的只读版——与 DeleteApp 受理位同一
	// DrivingByApp 谓词）：冲突在场仍出清单（预演面透出拒绝因，不拒答）。
	driving, err := svc.s.Deployments.DrivingByApp(ctx, appRow.ID)
	if err != nil {
		return nil, mapStateError(err, "deployment")
	}
	if len(driving) > 0 {
		out.DrivingConflict = true
		out.DrivingDetail = fmt.Sprintf("%d active deployment(s) drive this app; cancel them or wait for a terminal state before deleting", len(driving))
	}

	// 级联清单（铸/验共享单源 engine.TeardownCascade，修复批 F2）：将拆
	// 载体（当前生效渲染投影——engine.WorkloadID 寻址）+ 将删 hook 行 +
	// 将删 route 行（tombstone 事务的行为只读预演）。铸造侧不散写清单导出
	// ——peekTeardownConfirm 消费面按同函数重算 RequestDigest 比对，双源
	// 漂移即静默失配。
	cascade, err := svc.s.Engine.TeardownCascade(ctx, appRow.ID)
	if err != nil {
		return nil, mapStateError(err, "workloads")
	}
	for _, ent := range cascade {
		out.Cascade = append(out.Cascade, &deliveryv1.TeardownItem{
			Kind: ent.Kind, Name: ent.Name,
		})
	}
	// 保留清单（缺席语义显式化）：按生效 Spec 的引用集枚举四族；理由携带
	// 引用名集（脱敏红线——名可见值不进面）。
	baselineSpec, _, err := previewBaseline(ctx, svc.s, appRow.ID)
	if err != nil {
		return nil, err
	}
	out.Kept = keptItems(baselineSpec)

	// 铸票：base 三锚（app 行版本 + hook 行 digest + route 行 ID 集摘要）。
	// RequestDigest 绑预演清单本体（engine.TeardownCascadeDigest——票面
	// 确认的恰是展示的清单，铸验同参）；PreviewDigest 再并入保留清单
	// （载荷核心）。
	base, err := svc.s.Engine.TeardownAnchors(ctx, appRow.ID)
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	cascadeLines := make([]string, 0, len(cascade))
	for _, ent := range cascade {
		cascadeLines = append(cascadeLines, fmt.Sprintf("%d:%s", ent.Kind, ent.Name))
	}
	keptLines := make([]string, 0, len(out.Kept))
	for _, item := range out.Kept {
		keptLines = append(keptLines, fmt.Sprintf("%d:%s", item.GetKind(), item.GetReason()))
	}
	ticket, ttl, err := svc.s.MintConfirmTicket(governance.ConfirmTicketPayload{
		Verb:          previewVerbDelete,
		TargetApp:     appRow.ID,
		RequestDigest: engine.TeardownCascadeDigest(cascade),
		Base:          base,
		PreviewDigest: sha256Hex([]byte(strings.Join(append(cascadeLines, keptLines...), "\n"))),
		Operator:      confirmOperator(ctx),
	})
	if err != nil {
		return nil, mapStateError(err, "ticket")
	}
	out.Ticket = ticket
	//nolint:gosec // TTL 是 600s 常量（confirmTicketTTL），int32 安全。
	out.TicketExpiresIn = int32(ttl.Seconds())
	return out, nil
}

// keptItems 枚举拆除语义下显式保留的资源族（缺席语义）：按生效 Spec 的
// 引用集回答「这些不会被删」——数据库（project 级聚合）、卷（placement
// 钉住与数据面）、Secret（write-only 材料）、上传产物（project 级材料，
// 内容寻址 blob）。未引用的族不进场（空集无信息量）；理由携带名集。
func keptItems(sp *specv1.AppSpec) []*deliveryv1.KeptItem {
	if sp == nil {
		return nil // 从未成功部署：无生效 Spec 可推导引用面
	}
	var dbs, vols, secrets []string
	uploadID := sp.GetSource().GetUpload().GetId()
	for _, p := range sp.GetProcesses() {
		dbs = appendUnique(dbs, p.GetDatabaseRefs()...)
		vols = appendUnique(vols, volumeIDsOf(p)...)
		secrets = appendUnique(secrets, p.GetSecretRefs()...)
	}
	var out []*deliveryv1.KeptItem
	if len(dbs) > 0 {
		out = append(out, &deliveryv1.KeptItem{
			Kind:   deliveryv1.KeptItemKind_KEPT_ITEM_KIND_DATABASE,
			Reason: fmt.Sprintf("managed databases are project-scoped and survive app deletion (referenced: %s)", strings.Join(sortedCopy(dbs), ", ")),
		})
	}
	if len(vols) > 0 {
		out = append(out, &deliveryv1.KeptItem{
			Kind:   deliveryv1.KeptItemKind_KEPT_ITEM_KIND_VOLUME,
			Reason: fmt.Sprintf("volumes are project-scoped and survive app deletion (mounted: %s)", strings.Join(sortedCopy(vols), ", ")),
		})
	}
	if len(secrets) > 0 {
		out = append(out, &deliveryv1.KeptItem{
			Kind:   deliveryv1.KeptItemKind_KEPT_ITEM_KIND_SECRET,
			Reason: fmt.Sprintf("secrets are write-only material and survive app deletion (referenced: %s)", strings.Join(sortedCopy(secrets), ", ")),
		})
	}
	if uploadID != "" {
		out = append(out, &deliveryv1.KeptItem{
			Kind:   deliveryv1.KeptItemKind_KEPT_ITEM_KIND_UPLOAD,
			Reason: fmt.Sprintf("uploaded source %s is project-scoped material and survives app deletion (content-addressed blob)", uploadID),
		})
	}
	return out
}

// volumeIDsOf 抽进程的卷挂载名集。
func volumeIDsOf(p *specv1.ProcessSpec) []string {
	out := make([]string, 0, len(p.GetVolumes()))
	for _, v := range p.GetVolumes() {
		out = append(out, v.GetVolumeId())
	}
	return out
}

// appendUnique 追加去重（保序——名集再统一排序）。
func appendUnique(dst []string, src ...string) []string {
	seen := make(map[string]bool, len(dst))
	for _, s := range dst {
		seen[s] = true
	}
	for _, s := range src {
		if !seen[s] {
			dst = append(dst, s)
			seen[s] = true
		}
	}
	return dst
}

// sortedCopy 返回排序副本（确定性理由文案）。
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// ScaleProcess 是进程副本伸缩的一等动词（B1s）：读当前生效 Spec → 改
// 目标进程 replicas → MergeOverlay 提交（引擎半边）；响应复用 Admission
// 判定附注（与 DeployResponse 同构）。豁免确认（Restart/Scale 豁免族）；
// 不进幂等执法面（去重走 admission 判定）。
func (svc *DeploymentsService) ScaleProcess(ctx context.Context, req *deliveryv1.ScaleProcessRequest) (*deliveryv1.ScaleProcessResponse, error) {
	if err := svc.s.authorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	// CEL（num.non_negative）已在受理面前拦截；叶子复核在引擎侧
	// ValidateProcess（纵深——descriptor 缺位形态同拒）。
	_, adm, err := svc.s.Engine.ScaleProcess(ctx, req.GetAppId(), req.GetProcess(), int64(req.GetReplicas()))
	if err != nil {
		switch {
		case errors.Is(err, engine.ErrProcessUnknown):
			return nil, apperr.New("E_NOT_FOUND",
				"process %q not found in app %s (unknown name or never deployed)",
				req.GetProcess(), req.GetAppId()).WithCause(err)
		case errors.Is(err, engine.ErrNoSuccessfulBaseline):
			return nil, apperr.New("E_NO_BASELINE",
				"app %s has no successful deployment to scale", req.GetAppId()).WithCause(err)
		}
		var verr *specValidationError
		if errors.As(err, &verr) {
			return nil, mapValidationError(err)
		}
		return nil, mapStateError(err, "revision")
	}
	return &deliveryv1.ScaleProcessResponse{Admission: admissionMsg(adm)}, nil
}

// specValidationError 是 spec.ValidationError 的类型锚（errors.As 判型用；
// 避免本文件直接依赖 domain/shared/spec 的导出细节——mapValidationError
// 单源持映射）。
type specValidationError = spec.ValidationError
