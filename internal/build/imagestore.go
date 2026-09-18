package build

// 镜像身份（T2.9，v0.1 免 registry）：本机镜像登记与 preflight。
//
// 登记：构建成功即登记——builds 行的 image_digest ↔ image_ref 就是
// digest→ref 映射（FindBuildsByDigest 读通道），部署引用一律
// `名称@sha256:<digest>`（D9；Spike A E5 实证单节点 swarm 该形态零 pull）。
//
// 清理策略（release-semantics §2.4：v0.1 无 registry、不自动清理镜像）：
// 平台**永不**自动删除本机镜像——回滚依赖历史 digest 仍在本地，自动清理会
// 把 E_IMAGE_UNAVAILABLE 从「显式 preflight 失败」变成「部署中途失败」。
// 容量治理列为 v0.2 显式人工动作（配合 W_ROLLBACK_IMAGE_RISK 提示保留或
// 重建）。

import (
	"context"
	"errors"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// WarningRollbackImageRisk 是镜像缺失时随 E_IMAGE_UNAVAILABLE 携带的警告码
// 字面量（注册表 W_ROLLBACK_IMAGE_RISK：提示保留镜像或重建）。
const WarningRollbackImageRisk = "W_ROLLBACK_IMAGE_RISK"

// PreflightResult 是镜像 preflight 的结果（供发布引擎消费的接口形态）。
type PreflightResult struct {
	// Ref 是被检查的镜像引用（tag 或 `名称@sha256:` 形态）。
	Ref string
	// Digest 是本机镜像 ID（`sha256:<hex>`；不可用时为空）。
	Digest string
	// Available 报告镜像在本机 daemon 可得。
	Available bool
	// Warning 是缺失时的警告码（W_ROLLBACK_IMAGE_RISK）；可得时为空。
	Warning string
}

// PreflightImage 检查镜像在本机的可得性（release-semantics §2.4 preflight
// 第一项：镜像可得性，在动底座之前失败）。
//   - 可得：返回 Available=true + digest；
//   - 缺失：返回 ErrImageNotFound 哨兵包裹的 E_IMAGE_UNAVAILABLE 信封
//     （context.warning = W_ROLLBACK_IMAGE_RISK），同时返回带警告标注的
//     结果供调用方渲染；
//   - 底座不可达等其他错误：原样返回（引擎侧按 E_RUNTIME_UNAVAILABLE 面
//     处理，本包不挪用镜像码语义）。
func PreflightImage(ctx context.Context, images ImageSource, ref string) (PreflightResult, error) {
	info, err := images.InspectImage(ctx, ref)
	if err == nil {
		return PreflightResult{Ref: ref, Digest: info.ID, Available: true}, nil
	}
	if !errors.Is(err, ErrImageNotFound) {
		return PreflightResult{Ref: ref}, err
	}
	appErr := apperr.New("E_IMAGE_UNAVAILABLE",
		"镜像 %s 在本机不可用（可能已被清理）；部署/回滚引用以 digest 为准，镜像缺失时该引用无法解析", ref).
		WithPhase("preflight").
		WithContext("warning", WarningRollbackImageRisk).
		WithCause(err)
	return PreflightResult{Ref: ref, Warning: WarningRollbackImageRisk}, appErr
}
