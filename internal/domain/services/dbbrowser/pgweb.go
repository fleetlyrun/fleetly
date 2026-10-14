package dbbrowser

import (
	"fmt"
	"net/url"
	"strings"
)

// pgwebTemplate 承接 postgres/pgvector（ADR-0051 决策 3；方言实证
// 2026-10-07：sosedoff/pgweb:0.17.0）：
//
//   - 凭证注入 = argv --url（无密码）+ --passfile（pgpass 秘密文件）——
//     argv 不落明文，密码只在 0444 文件；
//   - 只读 = --readonly（工具层）+ 连接 URL options=-c
//     default_transaction_read_only=on（服务端会话执法——SHOW 实证 on，
//     绕过工具关键词过滤的写语句由 postgres 本身拒绝）；
//   - sslmode=disable：overlay 内网明文（swarm 2377 同口径；pgweb 缺省
//     require 会拒无 TLS 库）。
type pgwebTemplate struct{}

const (
	pgwebTag    = "sosedoff/pgweb:0.17.0"
	pgwebDigest = "sha256:a5256d416e2e8b92d69a4459058e3eca33a9f075d8325491644411d0bc3bd70b"
)

func (pgwebTemplate) Name() string                     { return "pgweb" }
func (pgwebTemplate) Image() string                    { return pgwebTag + "@" + pgwebDigest }
func (pgwebTemplate) ImageDigest() string              { return pgwebDigest }
func (pgwebTemplate) Port() int32                      { return 8080 }
func (pgwebTemplate) ReadOnlyEnforcement() Enforcement { return EnforcementSession }

// pgwebURL 铸连接 URL（无密码——密码走 pgpass 文件）。libpq 的 URI 解析器
// 不把 + 当空格（Go QueryEscape 的形态），options 值手工 %20 转义。
func pgwebURL(conn Conn, readonly bool) string {
	q := "sslmode=disable"
	if readonly {
		// 会话级服务端只读：postgres 对本会话拒绝一切写（含绕过工具
		// 过滤器的写语句）。
		q += "&options=" + strings.ReplaceAll(url.QueryEscape("-c default_transaction_read_only=on"), "+", "%20")
	}
	return fmt.Sprintf("postgresql://%s@%s:%d/%s?%s", conn.User, conn.Host, conn.Port, conn.DB, q)
}

func (t pgwebTemplate) Render(conn Conn, readonly bool) (Rendering, error) {
	if err := validateConn(conn); err != nil {
		return Rendering{}, err
	}
	// pgpass 行格式 host:port:db:user:password（libpq 逐字段匹配——首行
	// 命中即用；平台密码域 [0-9A-Za-z]，无冒号转义面）。
	pgpass := fmt.Sprintf("%s:%d:%s:%s:%s\n", conn.Host, conn.Port, conn.DB, conn.User, conn.Password)
	cmd := []string{
		"pgweb",
		"--url=" + pgwebURL(conn, readonly),
		"--passfile=/run/secrets/pgpass",
		"--bind=0.0.0.0",
		"--listen=8080",
		"--skip-open",
	}
	if readonly {
		cmd = append(cmd, "--readonly")
	}
	return Rendering{
		Command: cmd,
		Files:   map[string][]byte{"pgpass": []byte(pgpass)},
	}, nil
}
