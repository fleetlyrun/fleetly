package dbbrowser

import (
	"fmt"
)

// mongokuTemplate 承接 mongo（ADR-0051 决策 3；方言实证 2026-10-07：
// huggingface/mongoku:2.11.3）：
//
//   - 凭证注入 = env MONGOKU_DEFAULT_HOST=mongodb://user:password@host:port
//     ——**密码在 env**（方言边界同 redis-commander，ADR-0051 后果节记档）；
//   - 只读 = env MONGOKU_READ_ONLY_MODE=true——工具写端点门（构建产物
//     源码核对），非 mongo 服务端；
//   - 端口 = env MONGOKU_SERVER_PORT。
type mongokuTemplate struct{}

const (
	mongokuTag    = "huggingface/mongoku:2.11.3"
	mongokuDigest = "sha256:99486754b32cabd2d9c3d98318cccfdccdfb6822976f23a066d41c77d1b219c7"
)

func (mongokuTemplate) Name() string                     { return "mongoku" }
func (mongokuTemplate) Image() string                    { return mongokuTag + "@" + mongokuDigest }
func (mongokuTemplate) ImageDigest() string              { return mongokuDigest }
func (mongokuTemplate) Port() int32                      { return 8080 }
func (mongokuTemplate) ReadOnlyEnforcement() Enforcement { return EnforcementTool }

func (t mongokuTemplate) Render(conn Conn, readonly bool) (Rendering, error) {
	if err := validateConn(conn); err != nil {
		return Rendering{}, err
	}
	env := map[string]string{
		// 平台密码域 [0-9A-Za-z]——URL 无转义面。
		"MONGOKU_DEFAULT_HOST": fmt.Sprintf("mongodb://%s:%s@%s:%d", conn.User, conn.Password, conn.Host, conn.Port),
		"MONGOKU_SERVER_PORT":  "8080",
	}
	if readonly {
		env["MONGOKU_READ_ONLY_MODE"] = "true"
	}
	return Rendering{Env: env}, nil
}

// validateConn 是方言渲染面的共享闸（dbtemplate.validatePassword 同款
// 纵深防御：平台铸造域 [0-9A-Za-z]，hostile 值到不了这里也照样拒）。
func validateConn(conn Conn) error {
	if conn.Host == "" {
		return fmt.Errorf("dbbrowser: connection host must not be empty")
	}
	if conn.Port <= 0 {
		return fmt.Errorf("dbbrowser: connection port must be positive")
	}
	if conn.User == "" {
		return fmt.Errorf("dbbrowser: connection user must not be empty")
	}
	if err := validatePassword(conn.Password); err != nil {
		return err
	}
	if conn.DB == "" {
		return fmt.Errorf("dbbrowser: connection database must not be empty")
	}
	return nil
}
