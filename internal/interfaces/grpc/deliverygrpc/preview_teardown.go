package deliverygrpc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// PreviewTeardown 是 DeleteApp 的只读预演（B1 Teardown 面）：级联清单 +
// 保留清单 + 驱动权冲突 + 铸票。行级联镜像 DeleteApp 的 tombstone 事务
// 行为（App tombstone + Hooks.DeleteByApp + Routes 撤销）；保留清单是
// 缺席语义的显式透出（database/volume/secret/upload 四族按生效 Spec 的
// 引用集枚举——未被引用的族不进场，清单只回答「本 App 的拆除会碰到什么」）。
func (svc *DeploymentsService) PreviewTeardown(ctx context.Context, req *deliveryv1.PreviewTeardownRequest) (*deliveryv1.PreviewTeardownResponse, error) {
	// 行级授权（镜像 DeleteApp：载行 + 归属比对；scope apps:admin 由方法
	// policy 执法——预演动词不得低于被预演动词的权限门槛，设计稿 §1.2 A4）。
	appRow, err := svc.deps.Auth.AuthorizeAppID(ctx, req.GetAppId())
	if err != nil {
		return nil, err
	}
	out := &deliveryv1.PreviewTeardownResponse{}

	// 驱动权冲突（三层同谓词预检的只读版——与 DeleteApp 受理位同一
	// DrivingByApp 谓词）：冲突在场仍出清单（预演面透出拒绝因，不拒答）。
	driving, err := svc.deps.Deployments.DrivingByApp(ctx, appRow.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "deployment")
	}
	if len(driving) > 0 {
		out.DrivingConflict = true
		out.DrivingDetail = fmt.Sprintf("%d active deployment(s) drive this app; cancel them or wait for a terminal state before deleting", len(driving))
	}

	// 级联清单（铸/验共享单源 engine.TeardownCascade，修复批 F2）：将拆
	// 载体（当前生效渲染投影——api.WorkloadID 寻址）+ 将删 hook 行 +
	// 将删 route 行（tombstone 事务的行为只读预演）。铸造侧不散写清单导出
	// ——peekTeardownConfirm 消费面按同函数重算 RequestDigest 比对，双源
	// 漂移即静默失配。
	cascade, err := svc.deps.Deploy.TeardownCascade(ctx, appRow.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "workloads")
	}
	for _, ent := range cascade {
		out.Cascade = append(out.Cascade, &deliveryv1.TeardownItem{
			Kind: ent.Kind, Name: ent.Name,
		})
	}
	// 保留清单（缺席语义显式化）：按生效 Spec 的引用集枚举四族；理由携带
	// 引用名集（脱敏红线——名可见值不进面）。
	baselineSpec, _, err := previewBaseline(ctx, svc.deps.Deploy, appRow.ID)
	if err != nil {
		return nil, err
	}
	out.Kept = keptItems(ctx, svc.deps.Volumes, baselineSpec)

	// 铸票：base 三锚（app 行版本 + hook 行 digest + route 行 ID 集摘要）。
	// RequestDigest 绑预演清单本体（api.TeardownCascadeDigest——票面
	// 确认的恰是展示的清单，铸验同参）；PreviewDigest 再并入保留清单
	// （载荷核心）。
	base, err := svc.deps.Deploy.TeardownAnchors(ctx, appRow.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "app")
	}
	cascadeLines := make([]string, 0, len(cascade))
	for _, ent := range cascade {
		cascadeLines = append(cascadeLines, fmt.Sprintf("%d:%s", ent.Kind, ent.Name))
	}
	keptLines := make([]string, 0, len(out.Kept))
	for _, item := range out.Kept {
		keptLines = append(keptLines, fmt.Sprintf("%d:%s", item.GetKind(), item.GetReason()))
	}
	ticket, ttl, err := svc.deps.MintConfirm(governance.ConfirmTicketPayload{
		Verb:          previewVerbDelete,
		TargetApp:     appRow.ID,
		RequestDigest: api.TeardownCascadeDigest(cascade),
		Base:          base,
		PreviewDigest: grpcapi.SHA256Hex([]byte(strings.Join(append(cascadeLines, keptLines...), "\n"))),
		Operator:      grpcapi.ConfirmOperator(ctx),
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "ticket")
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
// 卷引用自 ADR-0097 起在冻结体内携带行 id——理由面按行回原名展示（name
// 是用户定位键，ADR-0092 决策 3；行已删的存量引用回退展示 id，诚实锚）。
func keptItems(ctx context.Context, volumes volume.Repo, sp *specv1.AppSpec) []*deliveryv1.KeptItem {
	if sp == nil {
		return nil // 从未成功部署：无生效 Spec 可推导引用面
	}
	var dbs, vols, secrets []string
	uploadID := sp.GetSource().GetUpload().GetId()
	for _, p := range sp.GetProcesses() {
		dbs = appendUnique(dbs, p.GetDatabaseRefs()...)
		for _, id := range volumeIDsOf(p) {
			vols = appendUnique(vols, volumeDisplayName(ctx, volumes, id))
		}
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

// volumeDisplayName 把卷行 id 解析回展示名（ADR-0097：冻结体携带行 id，
// 理由/清单面按 name 展示——ADR-0092 name 是用户定位键）。行缺席（存量
// 引用已删）回退原 id——id 是可复制稳定锚，诚实不隐藏。
func volumeDisplayName(ctx context.Context, volumes volume.Repo, id string) string {
	if row, err := volumes.Get(ctx, id); err == nil {
		return row.Name
	}
	return id
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
	if err := svc.deps.Auth.AuthorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	// CEL（num.non_negative）已在受理面前拦截；叶子复核在引擎侧
	// ValidateProcess（纵深——descriptor 缺位形态同拒）。
	_, adm, err := svc.deps.Deploy.ScaleProcess(ctx, req.GetAppId(), req.GetProcess(), int64(req.GetReplicas()))
	if err != nil {
		switch {
		case errors.Is(err, api.ErrProcessUnknown):
			return nil, apperr.New("E_NOT_FOUND",
				"process %q not found in app %s (unknown name or never deployed)",
				req.GetProcess(), req.GetAppId()).WithCause(err)
		case errors.Is(err, api.ErrNoSuccessfulBaseline):
			return nil, apperr.New("E_NO_BASELINE",
				"app %s has no successful deployment to scale", req.GetAppId()).WithCause(err)
		}
		var verr *specValidationError
		if errors.As(err, &verr) {
			return nil, grpcapi.MapValidationError(err)
		}
		return nil, grpcapi.MapStateError(err, "revision")
	}
	return &deliveryv1.ScaleProcessResponse{Admission: grpcapi.AdmissionMsg(adm)}, nil
}

// specValidationError 是 spec.ValidationError 的类型锚（errors.As 判型用；
// 避免本文件直接依赖 domain/shared/spec 的导出细节——mapValidationError
// 单源持映射）。
type specValidationError = spec.ValidationError
