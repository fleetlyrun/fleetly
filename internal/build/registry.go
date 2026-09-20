package build

// 平台 registry（zot）镜像管线（E1 多节点设计 §2.5/D-MN-11，E1-5）：
//
//	registry 模式（base_domain 非空，RegistryHost 配置非空）：
//	  构建产物 push 到 `registry.<base>/apps/<app>:b<buildid>`，digest 取自
//	  solve 响应，builds.image_ref 记 `registry.<base>/apps/<app>@sha256:<digest>`
//	  ——引擎按 digest 钉定引用直入 service spec（pinDigest 对已含 @ 的引用
//	  原样返回），worker 节点经 `--with-registry-auth` 分发的凭据拉取。
//	本地模式（RegistryHost 空）：
//	  v0.1 本地 digest 管线逐字不变（金样：既有 builder/buildopt 测试）。
//
// 本文件承载三块核心语义（第三方适配在 substrate/solve.go）：
//  1. 命名契约：registry 引用的仓库/tag/digest 三形态与判定谓词；
//  2. 凭据：`registry.auth_file` 凭据文件（`<user>:<password>` 明文形态，
//     与 ingress token 同形的 0600 平台生成文件；设计 §2.5——不入 SQLite，
//     轮换 = 重新生成 + 服务重建）；推送凭据经 buildkit session 注入
//     （solve.go），manifest HEAD 经 substrate 适配器携带；
//  3. 部署前哨双模式（D-MN-11）：本地模式 = PreflightImage（本机 inspect）；
//     registry 模式 = PreflightRegistry（manifest HEAD）——registry 不可达
//     → E_REGISTRY_UNAVAILABLE（503 快速失败不排队）；manifest 缺失 →
//     E_IMAGE_UNAVAILABLE 复用（语义同「回滚目标不可得」）；推送失败 →
//     E_REGISTRY_PUSH_FAILED。

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// RegistryRepoPrefix 是平台 registry 下应用镜像的路径前缀（设计 §2.5：
// `registry.<base>/apps/<app>` 命名契约）。
const RegistryRepoPrefix = "apps"

// RegistryBuildTagPrefix 是 registry 模式构建产物的 tag 前缀（设计 §2.5
// 数据流原文：`registry.<base>/apps/<app>:b<buildid>`）。
const RegistryBuildTagPrefix = "b"

// RegistryClient 是平台 registry 的最小前哨端口（架构 §2.8：端口在核心，
// HTTP 适配在 substrate）。ManifestHead 对 ref 做 manifest HEAD：
//   - 命中返回 manifest digest（`sha256:<hex>`，Docker-Content-Digest）；
//   - manifest 不存在归一为 ErrImageNotFound（适配器纪律同本地镜像端口）；
//   - 其余失败（连接拒绝/超时/凭据/5xx）原样返回原始错误——归一为
//     E_REGISTRY_UNAVAILABLE 信封是本包 PreflightRegistry 的职责。
type RegistryClient interface {
	ManifestHead(ctx context.Context, ref string) (string, error)
}

// RegistryRepo 返回 app 在平台 registry 下的仓库引用
// `registry.<base>/apps/<app>`（小写化——镜像仓库组件字符集限制）。
func RegistryRepo(host, app string) (string, error) {
	if host == "" {
		return "", fmtErr("registry host is empty (registry mode requires base_domain)")
	}
	repo := strings.ToLower(app)
	if !tagComponentRe.MatchString(repo) {
		return "", fmtErr("app name %q not valid as image repo component", app)
	}
	return host + "/" + RegistryRepoPrefix + "/" + repo, nil
}

// RegistryBuildRef 返回 registry 模式构建产物的推送引用
// `registry.<base>/apps/<app>:b<buildid>`。
func RegistryBuildRef(host, app, buildID string) (string, error) {
	repo, err := RegistryRepo(host, app)
	if err != nil {
		return "", err
	}
	if buildID == "" {
		return "", fmtErr("build id is empty (registry build ref requires a build id)")
	}
	return repo + ":" + RegistryBuildTagPrefix + buildID, nil
}

// RegistryDigestRef 返回 digest 钉定形态 `registry.<base>/apps/<app>@sha256:<digest>`
// （builds.image_ref 的 registry 模式记录形态；部署引用即本形态，D9）。
// digest 允许携带或不携带 `sha256:` 前缀，输出统一携带前缀。
func RegistryDigestRef(host, app, digest string) (string, error) {
	repo, err := RegistryRepo(host, app)
	if err != nil {
		return "", err
	}
	if digest == "" {
		return "", fmtErr("digest is empty (registry image accounting requires the pushed manifest digest)")
	}
	return repo + "@" + normalizeDigest(digest), nil
}

// IsRegistryImageRef 判定 ref 是否平台 registry 的构建产物引用
// （`<host>/apps/<app>@sha256:<hex>` 形态）。host 为空恒 false（本地模式
// 无 registry 引用——v0.1 等价断言的谓词面）。本地镜像（fleetly-local/…
// 或外部镜像引用）对平台 host 不命中前缀即 false。
func IsRegistryImageRef(ref, host string) bool {
	if host == "" || ref == "" {
		return false
	}
	prefix := host + "/" + RegistryRepoPrefix + "/"
	return strings.HasPrefix(ref, prefix) && strings.Contains(ref[len(prefix):], "@sha256:")
}

// normalizeDigest 统一 digest 的 `sha256:` 前缀形态。
func normalizeDigest(digest string) string {
	if strings.HasPrefix(digest, "sha256:") {
		return digest
	}
	return "sha256:" + digest
}

// RegistryCredentials 是平台 registry 的 Basic Auth 凭据（推送会话与
// manifest HEAD、swarm `--with-registry-auth` 编码共用）。
type RegistryCredentials struct {
	User     string
	Password string
}

// LoadRegistryCredentials 读取凭据文件（`<user>:<password>` 单行明文，
// 0600；registry.auth_file 的落盘契约，设计 §2.5「与 ingress token 同形」
// ——凭据由 ingress 部署器生成，本函数是消费端）。文件缺失/形态损坏
// 返回错误（调用方按推送失败/部署失败归因，不静默空凭据）。
func LoadRegistryCredentials(path string) (RegistryCredentials, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304：path 是平台配置的 registry.auth_file（registry.* 键）
	if err != nil {
		return RegistryCredentials{}, fmtErr("read registry credentials %s: %w", path, err)
	}
	line := strings.TrimSpace(string(raw))
	user, pass, ok := strings.Cut(line, ":")
	if !ok || user == "" || pass == "" || strings.ContainsAny(line, "\r\n") {
		return RegistryCredentials{}, fmtErr("registry credentials %s malformed (want a single user:password line)", path)
	}
	return RegistryCredentials{User: user, Password: pass}, nil
}

// PreflightResult 的 registry 形态复用 PreflightResult（Ref/Digest/Available/
// Warning 语义一致——Warning 仅在 manifest 缺失时携带 W_ROLLBACK_IMAGE_RISK，
// 语义同本地模式「回滚目标镜像不可得」）。

// PreflightRegistry 是 registry 模式的部署前哨（D-MN-11 双模式的 registry
// 腿）：对 ref 做 manifest HEAD——
//   - 命中：Available=true + manifest digest（`sha256:<hex>`）；
//   - ErrImageNotFound（manifest 缺失）：E_IMAGE_UNAVAILABLE 信封复用（语义
//     同「回滚目标镜像不可得」，context.warning = W_ROLLBACK_IMAGE_RISK）；
//   - 其余错误（registry 不可达/凭据/5xx）：E_REGISTRY_UNAVAILABLE 信封
//     （503，快速失败不排队——修复建议分层：registry 错 → 查 zot/网络/凭据）。
func PreflightRegistry(ctx context.Context, rc RegistryClient, ref string) (PreflightResult, error) {
	digest, err := rc.ManifestHead(ctx, ref)
	if err == nil {
		return PreflightResult{Ref: ref, Digest: normalizeDigest(digest), Available: true}, nil
	}
	if errors.Is(err, ErrImageNotFound) {
		appErr := apperr.New("E_IMAGE_UNAVAILABLE",
			"image %s has no manifest in the platform registry (it may never have been pushed, or registry data was reset); deploy/rollback references are digest-based and cannot be resolved while the manifest is missing", ref).
			WithStage("preflight").
			WithContext("warning", WarningRollbackImageRisk).
			WithCause(err)
		return PreflightResult{Ref: ref, Warning: WarningRollbackImageRisk}, appErr
	}
	return PreflightResult{Ref: ref}, registryUnavailable(ref, err)
}

// registryUnavailable 归一 registry 不可达信封（E_REGISTRY_UNAVAILABLE，
// 503——设计 §5.2/D-MN-11）。
func registryUnavailable(ref string, cause error) error {
	return apperr.New("E_REGISTRY_UNAVAILABLE",
		"platform registry did not answer while checking %s (deploy preflight fails fast; no queueing)", ref).
		WithStage("preflight").
		WithCause(cause)
}

// pushErrorMarker 是 buildkit 推送阶段失败的错误信息前缀（buildkit image
// exporter 的 push 错误以 "failed to push" 叙述——分类依据的唯一契约面；
// 非推送类 solve 错误不含该片段，仍归 E_BUILD_FAILED）。
const pushErrorMarker = "failed to push"

// ClassifyRegistryPushError 把 registry 模式的 solve 失败归一为应用错误：
// 推送类失败 → E_REGISTRY_PUSH_FAILED（网络/凭据/registry 故障，设计
// §5.2）；其余 solve 失败（上下文缺失/dockerfile 错误等构建本体错误）→
// E_BUILD_FAILED 原语义。分类依据是 buildkit 错误信息中的推送标记（上游
// 不提供错误码——以信息前缀为契约，已知保真度边界：极端情形把推送失败
// 误归 E_BUILD_FAILED 可接受，反向误归才会污染「改代码 vs 查 registry」
// 的分层建议）。
func ClassifyRegistryPushError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), pushErrorMarker) {
		return apperr.New("E_REGISTRY_PUSH_FAILED", "pushing build result to the platform registry failed: %v", err).
			WithStage("push").
			WithCause(err)
	}
	return err
}
