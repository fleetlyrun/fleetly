package imageregistry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrManifestNotFound 报告 registry 明确回答 manifest 不存在（404）——
// 调用点（substrate）据此在此次解析失败后回落本机 inspect。
var ErrManifestNotFound = errors.New("imageregistry: manifest not found")

// Credentials 是 registry 的 Basic Auth 凭证（匿名解析传 nil）。
type Credentials struct {
	Username string
	Password string
}

// 缺省重试预算：429/5xx 退避重试 3 次（300ms/600ms 递增等待）。
const (
	defaultMaxAttempts = 3
	defaultRetryDelay  = 300 * time.Millisecond
)

// manifestAccept 是 registry v2 manifest 请求的 Accept 形态集
// （index 与两种 manifest——tag 解析命中任意一层均有效）。
var manifestAccept = []string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}

// Client 是 registry v2 的最小解析客户端（HTTP 细节不出本包；凭证只进
// Authorization 与 token 请求的 Basic Auth）。
type Client struct {
	// HTTPClient 可替换（缺省 http.DefaultClient）；测试注入用。
	HTTPClient *http.Client
	// MaxAttempts 是 429/5xx 的重试总次数（含首次；<=0 取缺省 3）。
	MaxAttempts int
	// RetryDelay 是退避基数（第 n 次失败后等待 n×RetryDelay；<=0 取缺省 300ms）。
	RetryDelay time.Duration

	// baseURL 把 host 归一为请求基址（缺省 https://<host>；测试注入
	// http 基址的未导出缝——生产形态无 scheme 逃逸面）。
	baseURL func(host string) string
	// sleep 是退避等待（测试注入零等待）。
	sleep func(ctx context.Context, d time.Duration) error
}

// NewClient 构造解析客户端（缺省预算；测试经未导出字段改写）。
func NewClient() *Client { return &Client{} }

// httpClient 返回生效的 HTTP 客户端。
func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// maxAttempts 返回生效的重试总次数。
func (c *Client) maxAttempts() int {
	if c.MaxAttempts > 0 {
		return c.MaxAttempts
	}
	return defaultMaxAttempts
}

// retryDelay 返回生效的退避基数。
func (c *Client) retryDelay() time.Duration {
	if c.RetryDelay > 0 {
		return c.RetryDelay
	}
	return defaultRetryDelay
}

// base 返回 host 的请求基址（生产 = https；测试注入缝）。
func (c *Client) base(host string) string {
	if c.baseURL != nil {
		return strings.TrimSuffix(c.baseURL(host), "/")
	}
	return "https://" + host
}

// wait 执行一次退避等待（ctx 取消即返回）。
func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Resolve 把 tag 引用解析为 manifest digest（`sha256:<hex>`）：
//   - digest 引用直通（免网络）；
//   - 首次 manifest 请求匿名；401 时按 WWW-Authenticate 挑战应答
//     （Bearer → token 服务换 token 重试；Basic → Basic 重试）；
//   - 404 → ErrManifestNotFound；429/5xx 退避重试后仍失败 → 错误点名状态。
//
// 凭证绝不进入错误文本（负面测试钉死）；一切失败都由调用点归因。
func (c *Client) Resolve(ctx context.Context, ref Reference, creds *Credentials) (string, error) {
	if ref.Digest != "" {
		return ref.Digest, nil
	}
	if ref.Host == "" || ref.Repository == "" {
		return "", fmt.Errorf("imageregistry: cannot resolve an incomplete reference (host %q repository %q)", ref.Host, ref.Repository)
	}
	tag := ref.Tag
	if tag == "" {
		tag = "latest"
	}
	res, err := c.manifestRequest(ctx, ref, tag, "")
	if err != nil {
		return "", err
	}
	if res.StatusCode == http.StatusUnauthorized {
		challenge := res.Header.Get("WWW-Authenticate")
		drain(res)
		authorized, aerr := c.authorizeChallenge(ctx, ref.Host, challenge, creds)
		if aerr != nil {
			return "", aerr
		}
		if authorized == "" {
			if creds == nil {
				return "", fmt.Errorf("registry %s rejected the anonymous manifest request for %s:%s (unauthorized; configure registry credentials for this host)", ref.Host, ref.Repository, tag)
			}
			return "", fmt.Errorf("registry %s rejected the manifest request for %s:%s (unauthorized; check the configured registry credentials)", ref.Host, ref.Repository, tag)
		}
		res, err = c.manifestRequest(ctx, ref, tag, authorized)
		if err != nil {
			return "", err
		}
	}
	defer drain(res)

	switch res.StatusCode {
	case http.StatusOK:
		digest := res.Header.Get("Docker-Content-Digest")
		if digest == "" {
			return "", fmt.Errorf("registry %s returned manifest %s:%s without a Docker-Content-Digest header", ref.Host, ref.Repository, tag)
		}
		if !digestRe.MatchString(digest) {
			return "", fmt.Errorf("registry %s returned an invalid manifest digest %q for %s:%s", ref.Host, digest, ref.Repository, tag)
		}
		return digest, nil
	case http.StatusNotFound:
		return "", fmt.Errorf("%w: %s/%s:%s", ErrManifestNotFound, ref.Host, ref.Repository, tag)
	case http.StatusUnauthorized:
		return "", fmt.Errorf("registry %s rejected the manifest request for %s:%s (unauthorized; check the configured registry credentials)", ref.Host, ref.Repository, tag)
	default:
		return "", fmt.Errorf("registry %s answered %s for manifest %s:%s", ref.Host, res.Status, ref.Repository, tag)
	}
}

// manifestRequest 发出 manifest GET（429/5xx 退避重试）；返回的非重试
// 状态由调用点裁决（含 401 挑战）。响应体由调用点负责排空。
func (c *Client) manifestRequest(ctx context.Context, ref Reference, tag, authorization string) (*http.Response, error) {
	endpoint := c.base(ref.Host) + "/v2/" + ref.Repository + "/manifests/" + url.PathEscape(tag)
	var lastStatus string
	for attempt := 1; attempt <= c.maxAttempts(); attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("imageregistry: build manifest request: %w", err)
		}
		req.Header.Set("Accept", strings.Join(manifestAccept, ", "))
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		res, err := c.httpClient().Do(req)
		if err != nil {
			return nil, fmt.Errorf("registry %s unreachable while resolving %s:%s: %w", ref.Host, ref.Repository, tag, err)
		}
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
			lastStatus = res.Status
			drain(res)
			if attempt < c.maxAttempts() {
				if werr := c.wait(ctx, c.retryDelay()*time.Duration(attempt)); werr != nil {
					return nil, werr
				}
				continue
			}
			return nil, fmt.Errorf("registry %s answered %s for manifest %s:%s (retried %d times with backoff)", ref.Host, lastStatus, ref.Repository, tag, c.maxAttempts())
		}
		return res, nil
	}
	return nil, fmt.Errorf("registry %s exhausted manifest retries for %s:%s (last status %s)", ref.Host, ref.Repository, tag, lastStatus)
}

// authorizeChallenge 按 WWW-Authenticate 挑战计算重试请求的认证头：
//   - Bearer → token 服务换 token（Basic 凭证附于 token 请求）；
//   - Basic → Basic 挑战直配。
//
// 其余/缺失挑战返回空串（401 由调用点归因）。凭证材料零进错误文本。
func (c *Client) authorizeChallenge(ctx context.Context, host, challenge string, creds *Credentials) (string, error) {
	scheme, params := parseChallenge(challenge)
	switch strings.ToLower(scheme) {
	case "bearer":
		token, err := c.fetchBearerToken(ctx, host, params["realm"], params["service"], params["scope"], creds)
		if err != nil {
			return "", err
		}
		return "Bearer " + token, nil
	case "basic":
		return basicAuthHeader(creds), nil
	default:
		return "", nil
	}
}

// fetchBearerToken 从挑战给出的 token 服务换取 Bearer token（429/5xx
// 退避重试；凭证经 Basic Auth 附于 token 请求——匿名解析不带）。
func (c *Client) fetchBearerToken(ctx context.Context, host, realm, service, scope string, creds *Credentials) (string, error) {
	if realm == "" {
		return "", fmt.Errorf("registry %s answered a Bearer challenge without a realm (cannot obtain a token)", host)
	}
	endpoint, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("registry %s answered a Bearer challenge with an invalid realm %q: %w", host, realm, err)
	}
	query := endpoint.Query()
	if service != "" {
		query.Set("service", service)
	}
	if scope != "" {
		query.Set("scope", scope)
	}
	endpoint.RawQuery = query.Encode()

	var lastStatus string
	for attempt := 1; attempt <= c.maxAttempts(); attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return "", fmt.Errorf("imageregistry: build token request: %w", err)
		}
		if creds != nil {
			req.SetBasicAuth(creds.Username, creds.Password)
		}
		res, err := c.httpClient().Do(req)
		if err != nil {
			return "", fmt.Errorf("registry %s token service unreachable: %w", host, err)
		}
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
			lastStatus = res.Status
			drain(res)
			if attempt < c.maxAttempts() {
				if werr := c.wait(ctx, c.retryDelay()*time.Duration(attempt)); werr != nil {
					return "", werr
				}
				continue
			}
			return "", fmt.Errorf("registry %s token service answered %s (retried %d times with backoff)", host, lastStatus, c.maxAttempts())
		}
		if res.StatusCode != http.StatusOK {
			drain(res)
			return "", fmt.Errorf("registry %s token service answered %s (check the configured registry credentials)", host, res.Status)
		}
		raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		drain(res)
		if err != nil {
			return "", fmt.Errorf("registry %s token response unreadable: %w", host, err)
		}
		var payload struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return "", fmt.Errorf("registry %s token response is not JSON: %w", host, err)
		}
		if payload.Token != "" {
			return payload.Token, nil
		}
		if payload.AccessToken != "" {
			return payload.AccessToken, nil
		}
		return "", fmt.Errorf("registry %s token response carried neither token nor access_token", host)
	}
	return "", fmt.Errorf("registry %s token service exhausted retries (last status %s)", host, lastStatus)
}

// parseChallenge 解析 WWW-Authenticate 挑战（`Bearer realm="…",service="…",
// scope="…"` 与 Basic 形态；值可带引号或不带）。返回 scheme 与参数表
// （键小写）。
func parseChallenge(header string) (string, map[string]string) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", nil
	}
	scheme, rest, _ := strings.Cut(header, " ")
	params := map[string]string{}
	rest = strings.TrimSpace(rest)
	for rest != "" {
		eq := strings.Index(rest, "=")
		if eq < 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(rest[:eq]))
		rest = strings.TrimSpace(rest[eq+1:])
		var value string
		if strings.HasPrefix(rest, `"`) {
			rest = rest[1:]
			end := strings.Index(rest, `"`)
			if end < 0 {
				break
			}
			value = rest[:end]
			rest = strings.TrimPrefix(strings.TrimSpace(rest[end+1:]), ",")
		} else {
			if comma := strings.Index(rest, ","); comma >= 0 {
				value, rest = strings.TrimSpace(rest[:comma]), strings.TrimSpace(rest[comma+1:])
			} else {
				value, rest = strings.TrimSpace(rest), ""
			}
		}
		params[key] = value
	}
	return scheme, params
}

// basicAuthHeader 编码 Basic 认证头（creds 为空返回空串）。
func basicAuthHeader(creds *Credentials) string {
	if creds == nil {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds.Username+":"+creds.Password))
}

// drain 排空并关闭响应体（连接复用要求；错误语义不消费响应体）。
func drain(res *http.Response) {
	if res == nil || res.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
}
