// Package imageregistry 实现镜像引用解析与 registry v2 的最小客户端
// （IMPL-T1-2，DT-2「部署时镜像代拉」）：tag 引用经 registry API 解析为
// manifest digest（匿名与平台凭证两种 token flow），供部署路径 digest
// 钉定与 swarm 逐节点拉取使用。
//
// 边界纪律：
//   - 本包只做「引用 → digest」解析，不落盘、不缓存、不拉镜像层；
//   - 凭证只进入 Authorization 头与 token 请求的 Basic Auth，绝不进入
//     日志、错误文本或快照（负面测试钉死）；
//   - 网络面只有 HTTPS（测试经未导出注入缝指向 httptest，生产形态无逃逸）。
package imageregistry

import (
	"fmt"
	"regexp"
	"strings"
)

// Docker Hub 的规范 registry 端点：用户书写的 docker.io/index.docker.io
// 引用统一归一到 registry-1.docker.io（registry v2 API 真实端点）。
const (
	// dockerHubUserHost 是用户惯用的 Docker Hub registry 书写形态。
	dockerHubUserHost = "docker.io"
	// DockerHubHost 是 Docker Hub 的规范 registry 端点。
	DockerHubHost = "registry-1.docker.io"
)

// referenceComponentRe 是仓库路径组件的字符集（docker 同款小写字母表；
// 大写仓库名会被 registry 拒绝，解析期直接点名）。
var referenceComponentRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|[-]+)[a-z0-9]+)*$`)

// tagRe 是 tag 形态（首字符字母数字下划线，其余允许 . 与 -，≤128）。
var tagRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// digestRe 是 digest 形态（算法段 + 冒号 + 十六进制；sha256 为主流形态，
// 其他算法如实放行交由 registry 裁决）。
var digestRe = regexp.MustCompile(`^[a-z0-9]+(?:[.+_-][a-z0-9]+)*:[A-Fa-f0-9]{32,}$`)

// Reference 是解析后的镜像引用（registry v2 面所需的三要素）：
// Host 是规范 registry 端点（docker.io → registry-1.docker.io）；
// Repository 是仓库路径（Docker Hub 官方镜像补 library/ 前缀）；
// Tag 与 Digest 恰好一个生效（两者皆有 = tag+digest 钉定形态，Digest 优先）。
type Reference struct {
	Host       string
	Repository string
	Tag        string
	Digest     string
}

// Pinned 报告引用是否已带 digest（digest 钉定形态免解析直通）。
func (r Reference) Pinned() bool { return r.Digest != "" }

// Name 返回 `<host>/<repository>` 形态的仓库名（不含 tag/digest）。
func (r Reference) Name() string { return r.Host + "/" + r.Repository }

// String 返回规范化引用文本（Host/Repository 归一后的形态）。
func (r Reference) String() string {
	ref := r.Name()
	if r.Digest != "" {
		return ref + "@" + r.Digest
	}
	if r.Tag != "" {
		return ref + ":" + r.Tag
	}
	return ref
}

// NormalizeHost 把用户书写的 registry host 归一为引用可匹配的形态：
// 去空白、小写、去 scheme、去尾斜杠；docker.io 家族（docker.io /
// index.docker.io / registry.hub.docker.com）→ registry-1.docker.io。
// 空输入返回空串（调用点据此判定「清除设置」形态）。
func NormalizeHost(host string) string {
	normalized := strings.ToLower(strings.TrimSpace(host))
	normalized = strings.TrimPrefix(normalized, "https://")
	normalized = strings.TrimPrefix(normalized, "http://")
	normalized = strings.TrimSuffix(normalized, "/")
	switch normalized {
	case dockerHubUserHost, "index.docker.io", "registry.hub.docker.com":
		return DockerHubHost
	default:
		return normalized
	}
}

// Parse 解析镜像引用为 Reference：
//   - 显式 registry host（首段含 "." 或 ":"，或等于 localhost）；
//   - 无显式 host → Docker Hub（docker.io），官方镜像（无斜杠仓库）补
//     library/ 前缀；
//   - tag 与 digest 形态（`name@sha256:…`、`name:tag`；两者缺省 tag=latest）。
//
// 仓库路径限定 registry 合法字符集（小写）；形态非法返回错误，调用点
// （substrate）按「解析腿失败 → 回落本机 inspect」处理，不阻断部署。
func Parse(raw string) (Reference, error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return Reference{}, fmt.Errorf("imageregistry: image reference is empty")
	}
	if strings.Contains(ref, "://") {
		return Reference{}, fmt.Errorf("imageregistry: image reference %q must not carry a scheme", ref)
	}

	name := ref
	digest := ""
	if at := strings.Index(name, "@"); at >= 0 {
		name, digest = name[:at], name[at+1:]
		if digest == "" || !digestRe.MatchString(digest) {
			return Reference{}, fmt.Errorf("imageregistry: image reference %q carries an invalid digest", ref)
		}
	}

	tag := ""
	// tag 分隔冒号必须位于最后一个 "/" 之后（避免把 host:port 的冒号当 tag）。
	if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		name, tag = name[:colon], name[colon+1:]
		if !tagRe.MatchString(tag) {
			return Reference{}, fmt.Errorf("imageregistry: image reference %q carries an invalid tag", ref)
		}
	}

	host := dockerHubUserHost
	repository := name
	if slash := strings.Index(name, "/"); slash >= 0 {
		first := name[:slash]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			host = first
			repository = name[slash+1:]
		}
	}
	host = NormalizeHost(host)
	if host == "" || repository == "" {
		return Reference{}, fmt.Errorf("imageregistry: image reference %q has no repository name", ref)
	}
	if host == DockerHubHost && !strings.Contains(repository, "/") {
		repository = "library/" + repository
	}
	for _, component := range strings.Split(repository, "/") {
		if !referenceComponentRe.MatchString(component) {
			return Reference{}, fmt.Errorf("imageregistry: image reference %q has an invalid repository component %q (repository paths must be lowercase)", ref, component)
		}
	}
	if digest == "" && tag == "" {
		tag = "latest"
	}
	return Reference{Host: host, Repository: repository, Tag: tag, Digest: digest}, nil
}
