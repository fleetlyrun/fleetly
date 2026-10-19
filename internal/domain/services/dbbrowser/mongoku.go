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
	if err := requireConnUser(conn); err != nil {
		return Rendering{}, err
	}
	env := map[string]string{
		// 平台密码域 [0-9A-Za-z]——URL 无转义面。库段恒补 Conn.DB（R01-M1）：
		// Mongo 连接串无库段时 driver 的 authSource 缺省 admin，而平台账号
		// 建在业务库（dbtemplate mongo.go 的 init 脚本）——路径库即
		// authSource 缺省源，与 ConnURL 单真源同形。
		"MONGOKU_DEFAULT_HOST": fmt.Sprintf("mongodb://%s:%s@%s:%d/%s",
			conn.User, conn.Password, conn.Host, conn.Port, conn.DB),
		"MONGOKU_SERVER_PORT": "8080",
	}
	if readonly {
		env["MONGOKU_READ_ONLY_MODE"] = "true"
	}
	return Rendering{Env: env}, nil
}

// validateConn 是方言渲染面的共享闸（dbtemplate.validatePassword 同款
// 纵深防御：平台铸造域 [0-9A-Za-z]，hostile 值到不了这里也照样拒）。只含
// 全方言通用域——user 是否必填由方言自己判（redis 无用户名概念，
// requireConnUser；R01-H1†）。
func validateConn(conn Conn) error {
	if conn.Host == "" {
		return fmt.Errorf("dbbrowser: connection host must not be empty")
	}
	if conn.Port <= 0 {
		return fmt.Errorf("dbbrowser: connection port must be positive")
	}
	if err := validatePassword(conn.Password); err != nil {
		return err
	}
	if conn.DB == "" {
		return fmt.Errorf("dbbrowser: connection database must not be empty")
	}
	return nil
}

// requireConnUser 是"方言确实消费用户名"的附加闸（pgweb/adminer/mongoku
// 的连接串或材料按 user 铸造；redis-commander 的 env 方言不消费 user，
// 不设此闸——空用户名是其冻结连接串形态）。
func requireConnUser(conn Conn) error {
	if conn.User == "" {
		return fmt.Errorf("dbbrowser: connection user must not be empty")
	}
	return nil
}
