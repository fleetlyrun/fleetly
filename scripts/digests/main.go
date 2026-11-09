// Command digests 是数据服务引擎版本矩阵的 digest 取值/对账工具（ADR-0094
// 决策 6 SOP 条 2：追 patch = 本工具拉 registry manifest 回填注册表常量；
// design 2026-11-04 §2.6）。矩阵真源 = 三家族模板注册表（databasetemplate/
// pubsubtemplate/bucketstoretemplate，B5 起三族全覆）——本工具不复制 tag 表（引擎/
// 版本/tag/钉定 digest 全部运行时读自注册表），对账面自动跟随：每行输出
// 当前 index digest 与钉定值的 MATCH/DRIFT。
//
// registry 读取走 Docker Hub 匿名拉取面（token → tag manifest 的
// Docker-Content-Digest 响应头 = index digest，multi-arch 真源；与
// databasetemplate_live_test 同款协议、零 docker 依赖——mise 工具链内 go 承载，
// design §2.6「crane/go 脚本」的 go 形）。
//
// 用法（经 scripts/digests.sh 包装）：
//
//	scripts/digests.sh [engine [version]]     # 取值 + 对账（无参 = 全矩阵）
//	scripts/digests.sh -check [engine [version]]  # 漂移即退出码 1（CI 对账模式）
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
)

// registryFace 是三家族模板注册表的最小对账面（引擎值域 + 版本矩阵 +
// 绑版镜像引用——各包 Template 接口不同形，本工具只消费这三口）。
type registryFace struct {
	family   string
	engines  func() []string
	versions func(engine string) ([]dataservicetemplate.VersionSpec, bool)
	image    func(engine, version string) (string, error)
}

// registries 是三家族注册表的对账序（database → pubsub → bucketstore，
// 目录序同源）。
var registries = []registryFace{
	{
		family:   dataservicetemplate.FamilyDatabase,
		engines:  databasetemplate.Engines,
		versions: databasetemplate.VersionsFor,
		image: func(engine, version string) (string, error) {
			tpl, err := databasetemplate.For(engine, version)
			if err != nil {
				return "", err
			}
			return tpl.Image(), nil
		},
	},
	{
		family:   dataservicetemplate.FamilyPubSub,
		engines:  pubsubtemplate.Engines,
		versions: pubsubtemplate.VersionsFor,
		image: func(engine, version string) (string, error) {
			tpl, err := pubsubtemplate.For(engine, version)
			if err != nil {
				return "", err
			}
			return tpl.Image(), nil
		},
	},
	{
		family:   dataservicetemplate.FamilyBucketStore,
		engines:  bucketstoretemplate.Engines,
		versions: bucketstoretemplate.VersionsFor,
		image: func(engine, version string) (string, error) {
			tpl, err := bucketstoretemplate.For(engine, version)
			if err != nil {
				return "", err
			}
			return tpl.Image(), nil
		},
	},
}

// Docker Hub 匿名拉取面（registry 协议标准序：token → manifest）。
const (
	authEndpoint  = "https://auth.docker.io/token"
	registryAPI   = "https://registry-1.docker.io"
	requestBudget = 30 * time.Second
)

// manifestAcceptTypes：manifest 请求的 Accept 全集（index 与单 arch 双收
// ——tag 解析的目标形态是 index；钉定面另由 databasetemplate 门禁执法）。
const manifestAcceptTypes = "application/vnd.oci.image.index.v1+json," +
	"application/vnd.docker.distribution.manifest.list.v2+json," +
	"application/vnd.oci.image.manifest.v1+json," +
	"application/vnd.docker.distribution.manifest.v2+json"

func main() {
	check := flag.Bool("check", false, "exit 1 on any drift (pinned digest no longer matches the tag)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: digests [-check] [engine [version]]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	args := flag.Args()
	engineFilter, versionFilter := "", ""
	if len(args) > 0 {
		engineFilter = args[0]
	}
	if len(args) > 1 {
		versionFilter = args[1]
	}
	if len(args) > 2 {
		flag.Usage()
		os.Exit(2)
	}

	ctx := context.Background()
	hc := &http.Client{Timeout: requestBudget}
	drift := false
	for _, reg := range registries {
		for _, engine := range reg.engines() {
			if engineFilter != "" && engine != engineFilter {
				continue
			}
			versions, ok := reg.versions(engine)
			if !ok {
				continue // Engines 值域与注册表同源，不可达防御
			}
			for _, spec := range versions {
				if versionFilter != "" && spec.ID != versionFilter {
					continue
				}
				image, err := reg.image(engine, spec.ID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "digests: %s %s: bind template: %v\n", engine, spec.ID, err)
					os.Exit(1)
				}
				ref, pinned, _ := strings.Cut(image, "@") // tag@digest 双段律：首切即唯一切
				current, err := tagDigest(ctx, hc, ref)
				if err != nil {
					fmt.Fprintf(os.Stderr, "digests: %s %s: %v\n", engine, spec.ID, err)
					os.Exit(1)
				}
				state := "MATCH"
				if current != pinned {
					state = "DRIFT (pinned " + pinned + ")"
					drift = true
				}
				fmt.Printf("%s %s %s %s %s\n", engine, spec.ID, ref, current, state)
			}
		}
	}
	if *check && drift {
		os.Exit(1)
	}
}

// splitRepoTag 把 ref（repo:tag）拆为 registry API 面的规范名：无命名空间
// 的官方镜像在 registry API 面是 library/<名>（匿名 token 的 scope 与
// manifest 路径都认规范名，裸名 401——databasetemplate_live_test 同款口径）。
func splitRepoTag(ref string) (repo, tag string) {
	i := strings.LastIndex(ref, ":")
	if i > strings.LastIndex(ref, "/") {
		repo, tag = ref[:i], ref[i+1:]
	} else {
		repo = ref // 无 tag 段形态（当前值域不出现，防御）
	}
	if !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return repo, tag
}

// tagDigest 取 ref（repo:tag）当前指向的 index digest（Docker-Content-Digest
// 响应头——registry 对 manifest GET 的内容寻址真源，与 Accept 收到的形态
// 一致）。
func tagDigest(ctx context.Context, hc *http.Client, ref string) (string, error) {
	repo, tag := splitRepoTag(ref)
	q := url.Values{
		"service": {"registry.docker.io"},
		"scope":   {"repository:" + repo + ":pull"},
	}
	treq, err := http.NewRequestWithContext(ctx, http.MethodGet, authEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("build pull-token request for %s: %w", repo, err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if err := doJSON(hc, treq, &tok); err != nil {
		return "", fmt.Errorf("pull token for %s: %w", repo, err)
	}
	if tok.Token == "" {
		return "", fmt.Errorf("auth.docker.io returned an empty pull token for %s", repo)
	}
	mreq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/v2/%s/manifests/%s", registryAPI, repo, tag), nil)
	if err != nil {
		return "", fmt.Errorf("build manifest request for %s:%s: %w", repo, tag, err)
	}
	mreq.Header.Set("Authorization", "Bearer "+tok.Token)
	mreq.Header.Set("Accept", manifestAcceptTypes)
	resp, err := hc.Do(mreq) //nolint:gosec // 目标是注册表常量端点 + 引擎 tag，非用户可控 URL
	if err != nil {
		return "", fmt.Errorf("manifest request for %s:%s: %w", repo, tag, err)
	}
	defer func() { _ = resp.Body.Close() }()
	digest := resp.Header.Get("Docker-Content-Digest")
	if resp.StatusCode != http.StatusOK || digest == "" {
		return "", fmt.Errorf("manifest request for %s:%s: unexpected status %d (digest header empty: %t)",
			repo, tag, resp.StatusCode, digest == "")
	}
	return digest, nil
}

// doJSON 执行一次 GET 并解码 JSON（body 必关）。
func doJSON(hc *http.Client, req *http.Request, out any) error {
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
