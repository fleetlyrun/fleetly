package intake

// Registry 内容只读代理（IA v3 二期⑤b）：API 受理面 → capability.
// RegistryContent（zot v2 catalog/tags 代理）。端点凭证择优：per-Project
// 面（EndpointForProject——仓门禁 <projectID>/**，域隔离与推送同源）缺席
// 时回退平台凭证（adminPolicy 全域读）。前纲过滤与归属校验在本层（上游
// 无服务端过滤；<lower(projectID)>/ 前缀 = BuildRepo 单源）。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// RegistryCatalog 列项目前纲下的仓名（升序）。Registry 面或内容扩展接口未
// 装配 = 精确失败（不静默空清单——调用方据此显示 n/a 而非"无镜像"）。
// P5 wave-5 拆包导出：gRPC Registry 面经核门面委托（e.RegistryCatalog），
// 消费点零改。
func (in *Intake) RegistryCatalog(ctx context.Context, projectID string) ([]string, error) {
	content, ep, err := in.registryContentFor(ctx, projectID)
	if err != nil {
		return nil, err
	}
	repos, err := content.Catalog(ctx, ep)
	if err != nil {
		return nil, fmt.Errorf("registry catalog: %w", err)
	}
	prefix := BuildRepo(projectID, "") // lower(projectID) + "/"
	var out []string
	for _, repo := range repos {
		if strings.HasPrefix(repo, prefix) {
			out = append(out, repo)
		}
	}
	return out, nil
}

// RegistryTags 列项目前纲下仓名的 tag 事实。repository 必须收敛为平台
// 仓名（contract.ValidateRegistryRepository）：形态非法 = ErrInvalidRegistry-
// Repository（受理面 400），形态合法但越纲 = state.ErrNotFound（404——
// 跨项目探测不可达，与备份归属校验同口径）。校验真源随契约叶子（engine 去
// 应用模块 import 第七批），本包经 contract.* 直接消费。
func (in *Intake) RegistryTags(ctx context.Context, projectID, repository string) ([]capability.RegistryTag, error) {
	if err := contract.ValidateRegistryRepository(projectID, repository); err != nil {
		return nil, err
	}
	content, ep, err := in.registryContentFor(ctx, projectID)
	if err != nil {
		return nil, err
	}
	tags, err := content.Tags(ctx, ep, repository)
	if err != nil {
		var contentErr *capability.RegistryContentError
		if errors.As(err, &contentErr) && contentErr.Status == 404 {
			return nil, state.ErrNotFound // in-scope 仓名在上游不存在 → 精确 404（W-6）
		}
		return nil, fmt.Errorf("registry tags: %w", err)
	}
	return tags, nil
}

// registryContentFor 解析内容扩展接口与端点凭证（per-Project 面优先，平台
// 凭证回退）。端点凭证经 resolver 端口走核 resolve.go 解析器
// （registryEndpointFor 留核，resolutionface 锚不动）。
func (in *Intake) registryContentFor(ctx context.Context, projectID string) (capability.RegistryContent, capability.RegistryEndpoint, error) {
	if in.registry == nil {
		return nil, capability.RegistryEndpoint{}, fmt.Errorf(
			"%w: the managed registry is not configured (set registry.addr in the fleetlyd config to enable it)", contract.ErrRegistryUnavailable)
	}
	content := capability.FacesOf(in.registry).Content
	if content == nil {
		return nil, capability.RegistryEndpoint{}, fmt.Errorf(
			"%w: the registry provider does not offer the content face", contract.ErrRegistryUnavailable)
	}
	ep, err := in.registryEndpointFor(ctx, projectID)
	if err != nil {
		return nil, capability.RegistryEndpoint{}, err
	}
	return content, ep, nil
}
