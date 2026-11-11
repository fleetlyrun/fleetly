package githubapp

// client.go 是 GitHub App API 的领域契约面（ADR-0073 阶段 B；P3 客户端
// 接口化）：凭据载体、读回事实、错误哨兵与最小客户端接口住 domain，
// HTTP/JWT 实现住 infrastructure/github。interfaces 与 application 只
// 依赖本契约，不再为命名类型 import 基础设施实现包。

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// DefaultAPIBase 是 GitHub.com 的 API 基址（api_base 空 = 本值）。
const DefaultAPIBase = "https://api.github.com"

// Credentials 是一次 API 交互的凭据载体（聚合行解封后的明文形态；PEM
// 只驻内存）。EntityID 与 KeyFingerprint 组成 token 缓存键：轮换（指纹变）
// 或换行即自然失效，无需显式失效通道。
type Credentials struct {
	EntityID       string
	KeyFingerprint string
	AppID          int64
	InstallationID int64
	APIBase        string // 空 = DefaultAPIBase
	PrivateKeyPEM  []byte
}

// Installation 是受理即验证读到的安装事实（account.login 即入库的
// account_login——系统填充，用户不可写）。
type Installation struct {
	AppID          int64
	InstallationID int64
	AccountLogin   string
}

// Repository 是 installation 可达仓库的事实（full_name = "owner/repo"）。
type Repository struct {
	FullName string
	Private  bool
}

// ConvertedApp 是 manifest code 兑换回的 App 事实（ADR-0073 决策 8）：
// 私钥由 GitHub 生成、经本兑换一次性送达——调用方即收即封，不落日志。
type ConvertedApp struct {
	AppID int64
	Slug  string // 安装页地址段（install_url = github.com/apps/<slug>/installations/new）
	Name  string
	PEM   []byte
}

// UpstreamError 是 GitHub API 的非 2xx 应答（状态码 + 上游 message 字段）。
type UpstreamError struct {
	Status  int
	Message string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("github api %d: %s", e.Status, e.Message)
}

// ErrAppMismatch 是 installation 与 App ID 不配对（受理面精确报错的判定轴）。
var ErrAppMismatch = errors.New("github: installation does not belong to the given app id")

// Client 是 GitHub App 最小客户端契约：受理即验证、installation token
// 现铸、列可达仓库、manifest code 兑换。实现住 infrastructure/github。
type Client interface {
	ResolveInstallation(ctx context.Context, app *Credentials) (*Installation, error)
	InstallationToken(ctx context.Context, app *Credentials) (string, error)
	ListRepos(ctx context.Context, app *Credentials) ([]Repository, error)
	ConvertManifestCode(ctx context.Context, apiBase, code string) (*ConvertedApp, error)
}

// ParsePrivateKeyPEM 吃 PKCS#1（GitHub App 导出缺省）与 PKCS#8 两种 PEM；
// 受理面预校验与实现侧签名单源。
func ParsePrivateKeyPEM(pemBytes []byte) (*rsa.PrivateKey, error) {
	k, err := jwt.ParseRSAPrivateKeyFromPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("private key: must be a valid RSA PEM (PKCS#1 or PKCS#8): %v", err)
	}
	return k, nil
}
