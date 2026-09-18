package state

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// 写前直读（state-model §2.2 读契约）：写操作先直读底座并以对象版本作
// 乐观令牌；令牌失效（对象已被并发修改/删除）→ E_STATE_VERSION_CONFLICT
// （HTTP 409）。决策路径禁止消费观测缓存（nodes 表），令牌只能来自
// 直读——这是缓存陈旧被误当事实的唯一根治（D17）。
//
// 说明：ResolveVersion/CheckVersion 直读的对象 id 为底座对象标识
// （swarm node ID / swarm 服务名）；平台标识到底座标识的映射随 T2.14
// 适配器 Marker 落地，本阶段先立版本令牌纪律与契约错误码。

// VersionResolver 是写前直读端口。零状态，直读结果绝不落缓存。
type VersionResolver struct {
	docker DockerClient
}

// NewVersionResolver 构造直读端口。
func NewVersionResolver(d DockerClient) *VersionResolver {
	return &VersionResolver{docker: d}
}

// ResolveVersion 直读底座对象当前版本作乐观令牌。对象已不存在视为并发
// 冲突的特例（有人删了它）：返回 E_STATE_VERSION_CONFLICT（409，context
// 标注 reason=object_not_found），不发明清单外错误码。底层传输错误原样
// 透传（不打契约码）。
func (r *VersionResolver) ResolveVersion(ctx context.Context, kind ObjectKind, id string) (ObjectVersion, error) {
	if !kind.Valid() {
		return ObjectVersion{}, fmt.Errorf("state: unknown object kind %q", kind)
	}
	v, err := r.docker.ResolveObjectVersion(ctx, kind, id)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return ObjectVersion{}, versionConflict(kind, id, "object_not_found",
				"底座对象已不存在：可能在写前被并发删除，请刷新对象状态后重试")
		}
		return ObjectVersion{}, fmt.Errorf("state: resolve %s %s version: %w", kind, id, err)
	}
	return v, nil
}

// CheckVersion 校验调用方持有的令牌是否仍与底座当前版本一致：不一致
// （或对象已消失）返回 E_STATE_VERSION_CONFLICT（409）。
func (r *VersionResolver) CheckVersion(ctx context.Context, kind ObjectKind, id string, expected ObjectVersion) error {
	current, err := r.ResolveVersion(ctx, kind, id)
	if err != nil {
		return err
	}
	if current.Index != expected.Index {
		return versionConflict(kind, id, "version_mismatch",
			"对象已被并发修改（期望版本 "+strconv.FormatUint(expected.Index, 10)+
				"，底座当前版本 "+strconv.FormatUint(current.Index, 10)+"）：请重新读取后以新令牌重试")
	}
	return nil
}

// versionConflict 构造 E_STATE_VERSION_CONFLICT（409）应用错误，context
// 携带 kind/id/原因供调用方与 Agent 诊断。
func versionConflict(kind ObjectKind, id, reason, message string) *apperr.Error {
	return apperr.New("E_STATE_VERSION_CONFLICT", "%s", message).
		WithContext("kind", string(kind)).
		WithContext("id", id).
		WithContext("reason", reason)
}
