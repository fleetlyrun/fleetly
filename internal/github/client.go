// Package github 是 GitHub App 接入材料的最小客户端（ADR-0073 阶段 B）：
// PEM 私钥 → RS256 App JWT → installation token 现铸（短时效，进程内
// 缓存至到期前 5 分钟）+ 受理即验证的安装解析 + installation 仓库列面。
// stdlib HTTP；golang-jwt 仅签名单向（零解析面——本包不验任何外部 JWT）。
// 错误面：GitHub 非 2xx 归一为 UpstreamError（状态码 + 上游 message 字段，
// 不透传响应原文）；token 明文只进调用方手中，绝不进错误文本。
package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// DefaultAPIBase 是 api_base 缺省（github.com；GHES 由聚合行携带）。
const DefaultAPIBase = "https://api.github.com"

// appJTITTL / cacheSafetyMargin 是 App JWT 的有效窗（GitHub 上限 10 分钟）
// 与 token 缓存的安全提前量（到期前 5 分钟即视为过期——跨时钟域余量）。
const (
	appJWTTTL       = 10 * time.Minute
	appJWTLeeWay    = -60 * time.Second // iat 回拨，容忍小幅时钟漂移
	cacheSafetyMarg = 5 * time.Minute
)

// App 是一次交互的凭据载体（聚合行解封后的明文形态；PEM 只驻内存）。
type App struct {
	// EntityID 与 KeyFingerprint 组成 token 缓存键：轮换（指纹变）或换行
	// 即自然失效，无需显式失效通道。
	EntityID       string
	KeyFingerprint string
	AppID          int64
	InstallationID int64
	APIBase        string // 空 = DefaultAPIBase
	PrivateKeyPEM  []byte
}

// UpstreamError 是 GitHub API 的非 2xx 应答（状态码 + 上游 message 字段）。
type UpstreamError struct {
	Status  int
	Message string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("github api %d: %s", e.Status, e.Message)
}

// Client 铸 token / 解析安装 / 列仓库。零持久状态（HTTP + 内存缓存）。
type Client struct {
	http *http.Client
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]cachedToken
}

type cachedToken struct {
	token    string
	validTil time.Time
}

// NewClient 构造客户端（http/now 可注入：测试用假时钟与 httptest）。
func NewClient(httpClient *http.Client, now func() time.Time) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	return &Client{http: httpClient, now: now, cache: map[string]cachedToken{}}
}

// Installation 是受理即验证读到的安装事实（account.login 即入库的
// account_login——系统填充，用户不可写）。
type Installation struct {
	AppID          int64
	InstallationID int64
	AccountLogin   string
}

// ResolveInstallation 受理即验证：以私钥签 JWT 读 installation 对象，
// 校验 app_id 匹配并解析属主。404 = installation 不存在；app_id 不匹配 =
// ErrAppMismatch；其余非 2xx = UpstreamError。
func (c *Client) ResolveInstallation(ctx context.Context, app *App) (*Installation, error) {
	jwtToken, err := c.signAppJWT(app)
	if err != nil {
		return nil, err
	}
	var inst installationJSON
	if err := c.do(ctx, http.MethodGet, app, "/app/installations/"+strconv.FormatInt(app.InstallationID, 10),
		jwtToken, nil, &inst); err != nil {
		return nil, err
	}
	if inst.AppID != app.AppID {
		return nil, fmt.Errorf("%w: installation %d belongs to app %d, not %d", ErrAppMismatch,
			app.InstallationID, inst.AppID, app.AppID)
	}
	return &Installation{AppID: inst.AppID, InstallationID: inst.ID, AccountLogin: inst.Account.Login}, nil
}

// ErrAppMismatch 是 installation 与 App ID 不配对（受理面精确报错的判定轴）。
var ErrAppMismatch = fmt.Errorf("github: installation does not belong to the given app id")

// InstallationToken 现铸 installation token（缓存命中即返回；token 明文
// 只经返回值交付，不进日志与错误）。
func (c *Client) InstallationToken(ctx context.Context, app *App) (string, error) {
	cacheKey := app.EntityID + "\x00" + app.KeyFingerprint
	now := c.now()
	c.mu.Lock()
	hit, ok := c.cache[cacheKey]
	c.mu.Unlock()
	if ok && now.Before(hit.validTil) {
		return hit.token, nil
	}
	jwtToken, err := c.signAppJWT(app)
	if err != nil {
		return "", err
	}
	var minted struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := c.do(ctx, http.MethodPost, app,
		"/app/installations/"+strconv.FormatInt(app.InstallationID, 10)+"/access_tokens",
		jwtToken, nil, &minted); err != nil {
		return "", err
	}
	if minted.Token == "" {
		return "", fmt.Errorf("github: token minting returned an empty token")
	}
	validTil := now.Add(time.Hour) // 防线：expires_at 缺失/不可解析时按 1h 口径
	if minted.ExpiresAt != "" {
		if exp, perr := time.Parse(time.RFC3339, minted.ExpiresAt); perr == nil {
			validTil = exp
		}
	}
	validTil = validTil.Add(-cacheSafetyMarg)
	c.mu.Lock()
	c.cache[cacheKey] = cachedToken{token: minted.Token, validTil: validTil}
	c.mu.Unlock()
	return minted.Token, nil
}

// Repo 是 installation 可达仓库的事实（full_name = "owner/repo"）。
type Repo struct {
	FullName string
	Private  bool
}

// ListRepos 列 installation 可达仓库（分页拉全；受理验证面与 console
// Repos 面板共用）。鉴权 = installation token（/installation/repositories
// 不认 App JWT——401 Bad credentials；真机咬出后修正）。
func (c *Client) ListRepos(ctx context.Context, app *App) ([]Repo, error) {
	token, err := c.InstallationToken(ctx, app)
	if err != nil {
		return nil, err
	}
	var out []Repo
	for page := 1; ; page++ {
		var pageResp struct {
			Repositories []struct {
				FullName string `json:"full_name"`
				Private  bool   `json:"private"`
			} `json:"repositories"`
		}
		path := "/installation/repositories?per_page=100&page=" + strconv.Itoa(page)
		if err := c.do(ctx, http.MethodGet, app, path, token, nil, &pageResp); err != nil {
			return nil, err
		}
		for _, r := range pageResp.Repositories {
			out = append(out, Repo{FullName: r.FullName, Private: r.Private})
		}
		if len(pageResp.Repositories) < 100 {
			return out, nil
		}
	}
}

// signAppJWT 解析 PEM 并签 App JWT（iss=app_id 文本形态；每次现签——
// RSA 解析亚毫秒级，不值得第二个缓存面）。
func (c *Client) signAppJWT(app *App) (string, error) {
	key, err := ParsePrivateKeyPEM(app.PrivateKeyPEM)
	if err != nil {
		return "", err
	}
	now := c.now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iat": now.Add(appJWTLeeWay).Unix(),
		"exp": now.Add(appJWTTTL).Unix(),
		"iss": strconv.FormatInt(app.AppID, 10),
	})
	return token.SignedString(key)
}

// ParsePrivateKeyPEM 吃 PKCS#1（GitHub App 导出缺省）与 PKCS#8 两种 PEM；
// 受理面预校验与本包签名单源。
func ParsePrivateKeyPEM(pemBytes []byte) (*rsa.PrivateKey, error) {
	k, err := jwt.ParseRSAPrivateKeyFromPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("private key: must be a valid RSA PEM (PKCS#1 or PKCS#8): %v", err)
	}
	return k, nil
}

// do 执行一次 GitHub API 调用（鉴权头 + API 版本钉死；非 2xx 归一
// UpstreamError——响应体只取 message 字段）。token 不进任何错误文本。
func (c *Client) do(ctx context.Context, method string, app *App, path, bearer string, body any, out any) error {
	base := app.APIBase
	if base == "" {
		base = DefaultAPIBase
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "fleetly")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github api %s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // 应答体已读完或放弃，关闭错误无处置面
	if resp.StatusCode >= 300 {
		var errBody struct {
			Message string `json:"message"`
		}
		msg := resp.Status
		if json.NewDecoder(io.LimitReader(resp.Body, 8<<10)).Decode(&errBody) == nil && errBody.Message != "" {
			msg = errBody.Message
		}
		return &UpstreamError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
			return fmt.Errorf("github api %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

// ConvertedApp 是 manifest code 兑换回的 App 事实（ADR-0073 决策 8）：
// 私钥由 GitHub 生成、经本兑换一次性送达——调用方即收即封，不落日志。
type ConvertedApp struct {
	AppID int64
	Slug  string // 安装页地址段（install_url = github.com/apps/<slug>/installations/new）
	Name  string
	PEM   []byte
}

// ConvertManifestCode 用一次性 code 兑换 manifest 创建的 App 凭据（无鉴权
// 端点——code 本身即秘密，单次有效；apiBase 空 = github.com 缺省，测试经
// 注入指向假件）。错误归一 UpstreamError。
func (c *Client) ConvertManifestCode(ctx context.Context, apiBase, code string) (*ConvertedApp, error) {
	var resp struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
		PEM  string `json:"pem"`
	}
	app := &App{APIBase: apiBase} // conversions 端点无鉴权；base 空 = 缺省
	if err := c.do(ctx, http.MethodPost, app,
		"/app-manifests/"+url.PathEscape(code)+"/conversions", "", nil, &resp); err != nil {
		return nil, err
	}
	if resp.ID == 0 || resp.PEM == "" {
		return nil, fmt.Errorf("github: manifest conversion returned an incomplete payload")
	}
	return &ConvertedApp{AppID: resp.ID, Slug: resp.Slug, Name: resp.Name, PEM: []byte(resp.PEM)}, nil
}

// installationJSON 是 GET /app/installations/{id} 的响应切片。
type installationJSON struct {
	ID      int64 `json:"id"`
	AppID   int64 `json:"app_id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
}
