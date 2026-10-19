package engine

// Registry 内容只读代理（IA v3 二期⑤b）：API 受理面 → capability.
// RegistryContent（zot v2 catalog/tags 代理）。端点凭证择优：per-Project
// 面（EndpointForProject——仓门禁 <projectID>/**，域隔离与推送同源）缺席
// 时回退平台凭证（adminPolicy 全域读）。前纲过滤与归属校验在本层（上游
// 无服务端过滤；<lower(projectID)>/ 前缀 = buildRepo 单源）。

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// registryRepoSegmentRe / registryRepoNameRe 是平台仓名双段白名单
// （R03-M13）：项目段 = lower(projectID)（ULID 小写形态），仓名段单段
// [a-z0-9._-]+。大小写混杂一律拒绝（与 registry 双段小写纪律一致）；
// `..`/`.` 段单列拒绝（白名单字符集本身允许点号，但 dot-segment 是路径
// 归一面的注入入口）。
var (
	registryRepoSegmentRe = regexp.MustCompile(`^[a-z0-9]+$`)
	registryRepoNameRe    = regexp.MustCompile(`^[a-z0-9._-]+$`)
)

// ValidateRegistryRepository 把 API 传入的 repository 收敛为平台仓名：
// 恰两段、全小写白名单、无 `.`/`..` 段、无 `%`/`?`/`#`/空白/多级路径。
// 形态非法 = ErrInvalidRegistryRepository（受理面 E_INVALID_ARGUMENT；
// 用户输入错不是运行期故障）；形态合法但不属于该项目 = state.ErrNotFound
// （跨项目探测不可达，不泄漏存在性）。
func ValidateRegistryRepository(projectID, repository string) error {
	segments := strings.Split(repository, "/")
	if len(segments) != 2 ||
		!registryRepoSegmentRe.MatchString(segments[0]) ||
		!registryRepoNameRe.MatchString(segments[1]) ||
		segments[1] == "." || segments[1] == ".." {
		return ErrInvalidRegistryRepository
	}
	if segments[0]+"/" != buildRepo(projectID, "") {
		return state.ErrNotFound
	}
	return nil
}

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

// RegistryTags 列项目前纲下仓名的 tag 事实。repository 必须收敛为平台
// 仓名（ValidateRegistryRepository）：形态非法 = ErrInvalidRegistryRepository
// （受理面 400），形态合法但越纲 = state.ErrNotFound（404——跨项目探测
// 不可达，与备份归属校验同口径）。
func (e *Engine) RegistryTags(ctx context.Context, projectID, repository string) ([]capability.RegistryTag, error) {
	if err := ValidateRegistryRepository(projectID, repository); err != nil {
		return nil, err
	}
	content, ep, err := e.registryContentFor(ctx, projectID)
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

// ErrRegistryUnavailable 是 Registry 读代理的"面缺席"哨兵族（P0-7.2）：
// Registry 未装配（registry.addr 未配）或 provider 无 content 子面——配置
// 级显式 off 态而非内部故障，经 API 映射 E_REGISTRY_UNAVAILABLE（503 档，
// E_BROWSE_DISABLED"显式 off 态"同款先例；此前裸 fmt.Errorf 落 E_INTERNAL
// 500，把配置事实伪装成内部故障）。
var ErrRegistryUnavailable = errors.New("the managed registry read proxy is unavailable")

// registryContentFor 解析内容子面与端点凭证（per-Project 面优先，平台
// 凭证回退）。
func (e *Engine) registryContentFor(ctx context.Context, projectID string) (capability.RegistryContent, capability.RegistryEndpoint, error) {
	if e.registry == nil {
		return nil, capability.RegistryEndpoint{}, fmt.Errorf(
			"%w: the managed registry is not configured (set registry.addr in the fleetlyd config to enable it)", ErrRegistryUnavailable)
	}
	content := capability.FacesOf(e.registry).Content
	if content == nil {
		return nil, capability.RegistryEndpoint{}, fmt.Errorf(
			"%w: the registry provider does not offer the content face", ErrRegistryUnavailable)
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
