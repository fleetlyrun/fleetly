package acmedns

// Cloudflare API v4 插件（D-V3W5-3）：api.cloudflare.com/client/v4，
// API Token 认证（最小权限 Zone.DNS Edit）。
//
// API 形态实证（官方文档 developers.cloudflare.com/api/，2026-09 核对）：
//   - 认证：Authorization: Bearer <token>（API Token 形态）；
//   - 应答信封恒 {"success":bool,"errors":[{"code":int,"message":str}...],
//     "result":...}——HTTP 非 2xx 或 success=false 即错误（errors[0] 进
//     错误文案）；
//   - zone 解析：GET /zones?name=<域名>（name 过滤是精确匹配）→
//     result[0].id；
//   - 建记录：POST /zones/{zone_id}/dns_records，体 {"type":"TXT",
//     "name":<完整记录名>,"content":<值>,"ttl":120}（ttl=1 为 automatic，
//     有最小值约束——取 120 恒合法）；
//   - 列记录：GET /zones/{zone_id}/dns_records?type=TXT&name=<记录名>
//     （name 过滤精确匹配，result 为记录数组，含 id/content）；
//   - 删记录：DELETE /zones/{zone_id}/dns_records/{id}。
//
// 错误形态：信封 errors 逐条如实拼接（code+message；凭证材料零出现）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// cloudflareAPIBase 是 Cloudflare API v4 的官方基址。
const cloudflareAPIBase = "https://api.cloudflare.com/client/v4"

// cloudflareRecordTTL 是挑战 TXT 的 TTL（秒；文档口径 60..86400，120 恒合法）。
const cloudflareRecordTTL = 120

// cloudflare 是 Cloudflare Provider。
type cloudflare struct {
	apiToken string
	opts     options
}

// newCloudflare 构造 Cloudflare Provider（credentials = {"api_token":"..."}）。
func newCloudflare(credentials []byte, opts ...Option) (Provider, error) {
	cred, err := parseCredentials(credentials)
	if err != nil {
		return nil, err
	}
	o := options{httpClient: http.DefaultClient}
	for _, fn := range opts {
		fn(&o)
	}
	if o.httpClient == http.DefaultClient {
		o.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &cloudflare{apiToken: cred.APIToken, opts: o}, nil
}

// cloudflareEnvelope 是 v4 应答信封（result 按需二次解码——Raw 保真）。
type cloudflareEnvelope struct {
	Success bool              `json:"success"`
	Errors  []cloudflareError `json:"errors"`
	Result  json.RawMessage   `json:"result"`
}

type cloudflareError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// cloudflareZone 是 zones 列表的 result 元素（取 id）。
type cloudflareZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// cloudflareRecord 是 dns_records 的 result 元素。
type cloudflareRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// call 执行一次 JSON 请求并校验信封（success=false 或 HTTP 非 2xx → 错误）。
// out 非 nil 时接收 result 段二次解码。
func (c *cloudflare) call(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("acmedns: cloudflare encode request: %w", err)
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, reader)
	if err != nil {
		return fmt.Errorf("acmedns: cloudflare build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", dnspodUserAgent)
	resp, err := c.opts.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("acmedns: cloudflare %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("acmedns: cloudflare %s %s read response: %w", method, path, err)
	}
	var env cloudflareEnvelope
	if jerr := json.Unmarshal(raw, &env); jerr != nil {
		return fmt.Errorf("acmedns: cloudflare %s %s: HTTP %s with non-JSON body: %w", method, path, resp.Status, jerr)
	}
	if !env.Success || resp.StatusCode >= 300 {
		return fmt.Errorf("acmedns: cloudflare %s %s failed (HTTP %s): %s",
			method, path, resp.Status, c.formatErrors(env.Errors))
	}
	if out != nil && len(env.Result) > 0 {
		if jerr := json.Unmarshal(env.Result, out); jerr != nil {
			return fmt.Errorf("acmedns: cloudflare %s %s decode result: %w", method, path, jerr)
		}
	}
	return nil
}

// formatErrors 拼接信封错误（code+message 逐条；无错误条目给占位——信封
// success=false 而 errors 空的畸形形态不静默）。
func (c *cloudflare) formatErrors(errs []cloudflareError) string {
	if len(errs) == 0 {
		return "unknown error (empty errors envelope)"
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, fmt.Sprintf("code=%d message=%q", e.Code, e.Message))
	}
	return strings.Join(parts, "; ")
}

// baseURL 是 API 基址（注入覆盖优先，官方端点兜底）。
func (c *cloudflare) baseURL() string {
	if c.opts.baseURL != "" {
		return c.opts.baseURL
	}
	return cloudflareAPIBase
}

// zoneFor 把记录名解析为 zone id：候选最长优先逐个 GET /zones?name=
// （name 过滤精确匹配），首个命中返回。
func (c *cloudflare) zoneFor(ctx context.Context, recordName string) (string, error) {
	for _, candidate := range candidateSuffixes(recordName) {
		q := url.Values{"name": {candidate}}
		var zones []cloudflareZone
		if err := c.call(ctx, http.MethodGet, "/zones?"+q.Encode(), nil, &zones); err != nil {
			return "", err // zone 查询本身失败（网络/鉴权）：如实上抛——鉴权错与无 zone 是不同事实
		}
		if len(zones) > 0 {
			return zones[0].ID, nil
		}
	}
	return "", fmt.Errorf("acmedns: cloudflare: no zone accessible to this token covers %q", recordName)
}

// Present 在 fqdn 建 TXT 记录 value（zone 解析 → 建记录）。
func (c *cloudflare) Present(ctx context.Context, fqdn, value string) error {
	name := trimTrailingDot(fqdn)
	zoneID, err := c.zoneFor(ctx, name)
	if err != nil {
		return err
	}
	var created cloudflareRecord
	return c.call(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", map[string]any{
		"type":    "TXT",
		"name":    name,
		"content": value,
		"ttl":     cloudflareRecordTTL,
	}, &created)
}

// CleanUp 删 fqdn 上值等于 value 的 TXT 记录（列记录精确过滤 → 逐条值
// 匹配 DELETE；无匹配 = 幂等 no-op）。
func (c *cloudflare) CleanUp(ctx context.Context, fqdn, value string) error {
	name := trimTrailingDot(fqdn)
	zoneID, err := c.zoneFor(ctx, name)
	if err != nil {
		return err
	}
	q := url.Values{"type": {"TXT"}, "name": {name}}
	var records []cloudflareRecord
	if err := c.call(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records?"+q.Encode(), nil, &records); err != nil {
		return err
	}
	for _, r := range records {
		if r.Content != value {
			continue
		}
		if err := c.call(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+url.PathEscape(r.ID), nil, nil); err != nil {
			return err
		}
	}
	return nil
}
