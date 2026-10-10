package zot

// RegistryContent 只读代理实现（IA v3 二期⑤b）：/v2/_catalog 与
// /v2/<repo>/tags/list + manifest 逐 tag 事实（digest/压缩大小/config
// created）。basic auth 沿调用方传入的端点凭证（平台凭证全域可读、
// per-Project 凭证门禁自家前纲——调用方择一注入，本实现不解析归属）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// contentTimeout 是单次上游请求上限（catalog = 1 次；tags = 1 + 2N 次
// manifest/config——带总界防逐 tag 放大，victoriametrics queryTimeout
// 同款常量文化）。
const contentTimeout = 30 * time.Second

// maxTagsPerRepo 是单仓名 tag 事实解析的收敛上限（防御面：上限外 tag 只
// 给名字不给事实——分页属产品化批次）。
const maxTagsPerRepo = 200

// 编译期断言：RegistryContent 可选子面。
var _ capability.RegistryContent = (*Provider)(nil)

// Catalog 实现 RegistryContent：GET /v2/_catalog → repositories（升序，
// 上游已序则原样保序——双保险排序）。
func (p *Provider) Catalog(ctx context.Context, ep capability.RegistryEndpoint) ([]string, error) {
	var payload struct {
		Repositories []string `json:"repositories"`
	}
	if err := p.contentGet(ctx, ep, "/v2/_catalog", &payload); err != nil {
		return nil, err
	}
	out := payload.Repositories
	sort.Strings(out)
	return out, nil
}

// Tags 实现 RegistryContent：tags/list 名单 + 逐 tag manifest（digest 头
// 与压缩大小）+ config blob（created）。index/未知 media type 的 tag 无
// 单 tag 事实——诚实留空（PushedAt ""、SizeBytes 0）。解析失败的单 tag
// 不拖垮整列（digest 失败同样只降级该行）。
func (p *Provider) Tags(ctx context.Context, ep capability.RegistryEndpoint, repository string) ([]capability.RegistryTag, error) {
	var payload struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := p.contentGet(ctx, ep, "/v2/"+repository+"/tags/list", &payload); err != nil {
		return nil, err
	}
	names := payload.Tags
	if len(names) > maxTagsPerRepo {
		names = names[:maxTagsPerRepo]
	}
	out := make([]capability.RegistryTag, 0, len(names))
	for _, name := range names {
		tag := capability.RegistryTag{Name: name}
		body, mediaType, digest, err := p.manifestGet(ctx, ep, repository, name)
		if err != nil {
			out = append(out, tag)
			continue
		}
		tag.Digest = digest
		if isIndex(mediaType) {
			// index 形态：单 tag 事实不可得（子 manifest 才携带层），诚实留空。
			out = append(out, tag)
			continue
		}
		var manifest struct {
			Config struct {
				Digest string `json:"digest"`
				Size   int64  `json:"size"`
			} `json:"config"`
			Layers []struct {
				Size int64 `json:"size"`
			} `json:"layers"`
		}
		if json.Unmarshal(body, &manifest) == nil {
			tag.SizeBytes = manifest.Config.Size
			for _, layer := range manifest.Layers {
				tag.SizeBytes += layer.Size
			}
			tag.PushedAt = p.configCreated(ctx, ep, repository, manifest.Config.Digest)
		}
		out = append(out, tag)
	}
	return out, nil
}

// isIndex 报告 manifest media type 是否 index/manifest-list 形态（子
// manifest 才携带层事实——单 tag 大小/时刻不可得）。
func isIndex(mediaType string) bool {
	return strings.Contains(mediaType, "manifest.list") || strings.Contains(mediaType, "image.index")
}

// configCreated 取 config blob 的 created 注记（一次上游 GET；失败诚实
// 空串——时间戳缺失不伪装成已知）。
func (p *Provider) configCreated(ctx context.Context, ep capability.RegistryEndpoint, repository, configDigest string) string {
	if configDigest == "" {
		return ""
	}
	var cfg struct {
		Created string `json:"created"`
	}
	if p.contentGet(ctx, ep, "/v2/"+repository+"/blobs/"+configDigest, &cfg) != nil {
		return ""
	}
	return cfg.Created
}

// contentGet 是上游 GET 的统一形态（basic auth + 带界 + JSON 解码 + 非
// 2xx 有界 snippet，victorialogs do/respError 同款纪律）。
func (p *Provider) contentGet(ctx context.Context, ep capability.RegistryEndpoint, path string, out any) error {
	cctx, cancel := context.WithTimeout(ctx, contentTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, "http://"+ep.Addr+path, nil)
	if err != nil {
		return fmt.Errorf("zot content: %w", err)
	}
	req.SetBasicAuth(ep.Cred.Username, ep.Cred.Secret)
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("zot content: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("zot content: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("zot content: %s: status %d: %s", path, resp.StatusCode, snippet(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("zot content: %s: decode: %w", path, err)
	}
	return nil
}

// manifestGet 是 manifest 请求（需要响应头：Docker-Content-Digest 与
// Content-Type——contentGet 的 JSON 解码形态不适用）。
func (p *Provider) manifestGet(ctx context.Context, ep capability.RegistryEndpoint, repository, reference string) (body []byte, mediaType, digest string, err error) {
	cctx, cancel := context.WithTimeout(ctx, contentTimeout)
	defer cancel()
	req, rerr := http.NewRequestWithContext(cctx, http.MethodGet, "http://"+ep.Addr+"/v2/"+repository+"/manifests/"+reference, nil)
	if rerr != nil {
		return nil, "", "", fmt.Errorf("zot content: %w", rerr)
	}
	req.SetBasicAuth(ep.Cred.Username, ep.Cred.Secret)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", "))
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("zot content: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", "", fmt.Errorf("zot content: read manifest: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", "", fmt.Errorf("zot content: manifest %s: status %d: %s", reference, resp.StatusCode, snippet(string(body)))
	}
	return body, resp.Header.Get("Content-Type"), resp.Header.Get("Docker-Content-Digest"), nil
}

// snippet 是有界错误正文（200 字节，victorialogs respError 同口径）。
func snippet(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
