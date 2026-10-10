package engine

// Registry 内容只读代理（IA v3 二期⑤b）：API 受理面 → capability.
// RegistryContent（zot v2 catalog/tags 代理）。端点凭证择优：per-Project
// 面（EndpointForProject——仓门禁 <projectID>/**，域隔离与推送同源）缺席
// 时回退平台凭证（adminPolicy 全域读）。前纲过滤与归属校验在本层（上游
// 无服务端过滤；<lower(projectID)>/ 前缀 = buildRepo 单源）。

import (
	"context"
	"fmt"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// RegistryCatalog 列项目前纲下的仓名（升序）。Registry 面或内容子面未
// 装配 = 精确失败（不静默空清单——调用方据此显示 n/a 而非"无镜像"）。
func (e *Engine) RegistryCatalog(ctx context.Context, projectID string) ([]string, error) {
	content, ep, err := e.registryContentFor(ctx, projectID)
	if err != nil {
		return nil, err
	}
	repos, err := content.Catalog(ctx, ep)
	if err != nil {
		return nil, fmt.Errorf("registry catalog: %w", err)
	}
	prefix := buildRepo(projectID, "") // lower(projectID) + "/"
	var out []string
	for _, repo := range repos {
		if strings.HasPrefix(repo, prefix) {
			out = append(out, repo)
		}
	}
	return out, nil
}

// RegistryTags 列项目前纲下仓名的 tag 事实。repository 必须落在该项目
// 前纲下（越纲 = state.ErrNotFound——跨项目探测不可达，与备份归属校验
// 同口径；API 层 map 404）。
func (e *Engine) RegistryTags(ctx context.Context, projectID, repository string) ([]capability.RegistryTag, error) {
	prefix := buildRepo(projectID, "")
	if repository == "" || !strings.HasPrefix(strings.ToLower(repository), prefix) {
		return nil, state.ErrNotFound
	}
	content, ep, err := e.registryContentFor(ctx, projectID)
	if err != nil {
		return nil, err
	}
	tags, err := content.Tags(ctx, ep, strings.ToLower(repository))
	if err != nil {
		return nil, fmt.Errorf("registry tags: %w", err)
	}
	return tags, nil
}

// registryContentFor 解析内容子面与端点凭证（per-Project 面优先，平台
// 凭证回退）。
func (e *Engine) registryContentFor(ctx context.Context, projectID string) (capability.RegistryContent, capability.RegistryEndpoint, error) {
	if e.registry == nil {
		return nil, capability.RegistryEndpoint{}, fmt.Errorf("registry: the managed registry is not configured (set config registry.addr to enable it)")
	}
	content := capability.FacesOf(e.registry).Content
	if content == nil {
		return nil, capability.RegistryEndpoint{}, fmt.Errorf("registry: the registry provider does not offer the content face")
	}
	ep, err := e.registryEndpointFor(ctx, projectID)
	if err != nil {
		return nil, capability.RegistryEndpoint{}, err
	}
	return content, ep, nil
}

// registryEndpointFor 解析代理调用凭证（per-Project 优先——accessControl
// 门禁与构建推送同源；平台凭证兜底——adminPolicy 全域读）。
func (e *Engine) registryEndpointFor(ctx context.Context, projectID string) (capability.RegistryEndpoint, error) {
	faces := capability.FacesOf(e.registry)
	if faces.ProjectEndpoints != nil {
		if ep, err := faces.ProjectEndpoints.EndpointForProject(ctx, projectID); err == nil {
			return ep, nil
		}
		// per-Project 凭证铸造失败（盘面等）→ 平台凭证回退（只读代理，
		// 权限面更宽不构成提权——受理位已做行级授权）。
	}
	return e.registry.Endpoint(ctx)
}
