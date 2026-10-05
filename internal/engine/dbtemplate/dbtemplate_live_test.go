package dbtemplate_test

// VOLUME 契约 live 核对（2026-10-05 N2 评审 P2-1 执法补强；ADR-0045
// 执法补强节）：TestVolumeShadowContract 的预期表是硬编码快照，digest
// bump 后上游若改 VOLUME 声明，静态测试照绿——ADR-0045 决策 4 的"VOLUME
// 面不变"bump 射程约束是承诺不是执法（既有兜底只有 bump 批 CI 的
// e2e-backup 任务替换存活锚，动态且滞后）。本测试经 Docker Hub registry
// API（匿名拉取面）读钉定 index → 本平台 arch manifest → config blob 的
// Volumes 字段，与同一张 volumeShadowTable（唯一预期表，住
// dbtemplate_test.go）对账：上游 VOLUME 面变化即红。红了的处置口径
// （ADR-0045 决策 4）：patch 内不变量被破坏 = major 级变更，走版本矩阵
// 独立 ADR，不许静默改表。
//
// env gate：缺省 SKIP（CI 零网络依赖不红）；dbtemplate digest bump 批
// 必跑：FLEETLY_DBTEMPLATE_LIVE=1 go test ./internal/engine/dbtemplate/
// -run TestVolumeShadowContractLive（用法记档在 ADR-0045 执法补强节与
// staging runbook 的 bump 检查单）。
//
// repo/tag/digest 单源：从 tpl.Image() 拆解取得——adapter 钉定对常量的
// 唯一暴露面，即平台实际部署的引用。本文件不重抄钉定值（第二份表/值即
// 破口）；改 adapter 常量，live 核对自动跟随新 digest。
//
// 首跑实录（2026-10-05）：mongo 钉定镜像另声明 /data/configdb（在
// DataTarget /data/db 之外）——静态表自建表以来从未对账过全集，live 首跑
// 即红一枚。该路径即 N2 评审 P2-4 匿名卷泄漏台账的镜像遗产面（无数据
// 丢失面）：表按实测全集记档，静态契约三分派生消化（见 volumeShadowTable
// 注）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/engine/dbtemplate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveEnvGate 是 live 核对开关（=1 启用；缺省 SKIP）。
const liveEnvGate = "FLEETLY_DBTEMPLATE_LIVE"

// Docker Hub 匿名拉取面（registry 协议标准序：token → manifest → blob）。
const (
	dockerAuthEndpoint = "https://auth.docker.io/token"
	dockerRegistryAPI  = "https://registry-1.docker.io"
	liveRequestTimeout = 30 * time.Second
	liveOverallBudget  = 5 * time.Minute
)

// manifestAcceptTypes：manifest 请求的 Accept 全集（index 与单 arch
// manifest 双收——钉定 digest 理论上恒为 OCI index，直指单 manifest 是
// 防御形态）。
const manifestAcceptTypes = "application/vnd.oci.image.index.v1+json," +
	"application/vnd.docker.distribution.manifest.list.v2+json," +
	"application/vnd.oci.image.manifest.v1+json," +
	"application/vnd.docker.distribution.manifest.v2+json"

// registryManifest 是 index 与单 arch manifest 的并集解析面（区分依据：
// manifests 数组在场 = index；config 在场 = 单 arch manifest）。
type registryManifest struct {
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform *struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
		} `json:"platform"`
	} `json:"manifests"`
	Config *struct {
		Digest string `json:"digest"`
	} `json:"config"`
}

// imageConfigDoc 是镜像 config blob 的最小解析面（Volumes 键集 = VOLUME
// 声明路径集；值是元数据占位，不消费）。
type imageConfigDoc struct {
	Config struct {
		Volumes map[string]json.RawMessage `json:"Volumes"`
	} `json:"config"`
}

// splitPinnedImage 把 Image() 的 tag@digest 双段引用拆回 repo/tag 与
// digest（钉定对单源消费：live 核对读的就是部署引用本身，非重抄常量）。
// repo 走 docker 名字解析口径：无命名空间的官方镜像在 registry API 面是
// library/<名>（匿名 token 的 scope 与 manifest 路径都认规范名，裸名 401）。
func splitPinnedImage(image string) (repo, tag, digest string) {
	ref, dgst, _ := strings.Cut(image, "@") // digest 段不含 "@"，首切即唯一切
	digest = dgst
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo, tag = ref[:i], ref[i+1:]
	} else {
		repo = ref // 无 tag 段形态（当前值域不出现，防御）
	}
	if !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return repo, tag, digest
}

// liveDoJSON 执行一次 GET 并解码 JSON（body 必关；32MiB 读界防御）。
// step 进失败错误——引擎名 + repo + 步骤的清晰锚（用户可见文本英文）。
func liveDoJSON(ctx context.Context, hc *http.Client, engine, repo, step string, req *http.Request, out any) error {
	resp, err := hc.Do(req) //nolint:gosec // 目标是钉定常量端点 + registry 铸的 digest 路径，非用户可控 URL
	if err != nil {
		return fmt.Errorf("%s: %s request for %s: %w", engine, step, repo, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s request for %s: unexpected status %d", engine, step, repo, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out); err != nil {
		return fmt.Errorf("%s: decode %s response for %s: %w", engine, step, repo, err)
	}
	return nil
}

// livePullToken 取 Docker Hub 匿名拉取令牌（registry 协议握手第一步：
// scope 限 repository:<repo>:pull，只读匿名面，零凭据）。
func livePullToken(ctx context.Context, hc *http.Client, engine, repo string) (string, error) {
	q := url.Values{
		"service": {"registry.docker.io"},
		"scope":   {"repository:" + repo + ":pull"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dockerAuthEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("%s: build pull-token request for %s: %w", engine, repo, err)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := liveDoJSON(ctx, hc, engine, repo, "pull-token", req, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("%s: auth.docker.io returned an empty pull token for %s", engine, repo)
	}
	return out.Token, nil
}

// liveFetchManifest 按 digest 取 manifest（index 或单 arch 形态皆收）。
func liveFetchManifest(ctx context.Context, hc *http.Client, engine, repo, digest, token string) (*registryManifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/v2/%s/manifests/%s", dockerRegistryAPI, repo, digest), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build manifest request for %s@%s: %w", engine, repo, digest, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", manifestAcceptTypes)
	var out registryManifest
	if err := liveDoJSON(ctx, hc, engine, repo, "manifest", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// liveImageVolumes 走 registry 三步取镜像 VOLUME 声明路径集（排序稳定）：
// 匿名 token → 钉定 digest 的 manifest（index 形态则选本平台 arch 的子
// manifest 再走一层）→ config blob 的 Volumes 键集。
func liveImageVolumes(ctx context.Context, hc *http.Client, engine, repo, digest string) ([]string, error) {
	token, err := livePullToken(ctx, hc, engine, repo)
	if err != nil {
		return nil, err
	}
	manifest, err := liveFetchManifest(ctx, hc, engine, repo, digest, token)
	if err != nil {
		return nil, err
	}
	configDigest := ""
	if manifest.Config != nil && manifest.Config.Digest != "" {
		// 钉定 digest 直指单 arch manifest 的防御形态（当前五引擎全为 index）。
		configDigest = manifest.Config.Digest
	} else {
		// index 形态：选本平台（linux/<GOARCH>）的 manifest；attestation 类
		// "unknown/unknown" 条目自然跳过。
		found := false
		for _, m := range manifest.Manifests {
			if m.Platform == nil || m.Platform.OS != "linux" || m.Platform.Architecture != runtime.GOARCH {
				continue
			}
			arch, err := liveFetchManifest(ctx, hc, engine, repo, m.Digest, token)
			if err != nil {
				return nil, err
			}
			if arch.Config == nil || arch.Config.Digest == "" {
				return nil, fmt.Errorf("%s: arch manifest for %s@%s carries no config link", engine, repo, digest)
			}
			configDigest = arch.Config.Digest
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("%s: pinned index for %s@%s carries no linux/%s manifest", engine, repo, digest, runtime.GOARCH)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/v2/%s/blobs/%s", dockerRegistryAPI, repo, configDigest), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build config-blob request for %s@%s: %w", engine, repo, digest, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var doc imageConfigDoc
	if err := liveDoJSON(ctx, hc, engine, repo, "config-blob", req, &doc); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(doc.Config.Volumes))
	for p := range doc.Config.Volumes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

// TestVolumeShadowContractLive：五引擎钉定镜像的 VOLUME 集与
// volumeShadowTable 实测对账（N2 评审 P2-1 的核心验收——把"VOLUME 面不变"
// 从承诺变成可红的执法）。
func TestVolumeShadowContractLive(t *testing.T) {
	if os.Getenv(liveEnvGate) != "1" {
		t.Skipf("live registry check is opt-in; set %s=1 (mandatory on dbtemplate digest bump batches, ADR-0045)", liveEnvGate)
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveOverallBudget)
	defer cancel()
	hc := &http.Client{Timeout: liveRequestTimeout}
	for _, tc := range volumeShadowTable {
		t.Run(tc.engine, func(t *testing.T) {
			tpl, ok := dbtemplate.For(tc.engine)
			require.True(t, ok)
			repo, tag, digest := splitPinnedImage(tpl.Image())
			volumes, err := liveImageVolumes(ctx, hc, tc.engine, repo, digest)
			require.NoError(t, err, "%s: live registry read failed for %s:%s", tc.engine, repo, tag)
			assert.ElementsMatch(t, tc.imageVolumes, volumes,
				"%s: pinned image %s:%s@%s declares VOLUME %v, contract table expects %v — the upstream VOLUME face changed; "+
					"reassess the bump scope per ADR-0045 decision 4 (patch-scope invariants broken = major-grade change with its own ADR; "+
					"never edit volumeShadowTable silently)",
				tc.engine, repo, tag, digest, volumes, tc.imageVolumes)
		})
	}
}
