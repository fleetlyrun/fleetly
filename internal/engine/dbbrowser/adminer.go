package dbbrowser

import (
	"encoding/json"
	"fmt"
)

// adminerTemplate 承接 mysql（ADR-0051 决策 3；方言实证 2026-10-07：
// adminer:6.1.1 官方镜像）：
//
//   - 凭证注入 = 秘密文件 db-conn.json + vendored router.php（本包 embed，
//     assets/router.php）——凭证不进 env/argv/spec/URL（重定向只带
//     server/username/db 非密参数）；
//   - 命令 = php -S 0.0.0.0:8080 -t /var/www/html /run/secrets/router.php
//     （镜像 entrypoint 前置执行后 exec 本命令；php 内建服务器无 XFO，
//     但 adminer 自身 page_headers 硬编码 deny——iframe 不可用，新窗口
//     形态不受影响）；
//   - 只读 = **无方言**（EnforcementNone）——readonly 渲染是受理面错误，
//     Render fail-closed（ADR-0051 决策 6：mysql 只读浏览须 databases:write
//     门，直到服务端只读角色铸造落地）。
type adminerTemplate struct{}

const (
	adminerTag    = "adminer:6.1.1"
	adminerDigest = "sha256:74f29c416e148b98305e84446a18db7cd2c1038264dec464359d36774ef080ce"
)

func (adminerTemplate) Name() string                     { return "adminer" }
func (adminerTemplate) Image() string                    { return adminerTag + "@" + adminerDigest }
func (adminerTemplate) ImageDigest() string              { return adminerDigest }
func (adminerTemplate) Port() int32                      { return 8080 }
func (adminerTemplate) ReadOnlyEnforcement() Enforcement { return EnforcementNone }

func (t adminerTemplate) Render(conn Conn, readonly bool) (Rendering, error) {
	if err := validateConn(conn); err != nil {
		return Rendering{}, err
	}
	if readonly {
		return Rendering{}, fmt.Errorf("dbbrowser: adminer has no read-only dialect (enforcement none; read-write gate is at acceptance)")
	}
	connJSON, err := json.Marshal(map[string]string{
		"host":     conn.Host,
		"user":     conn.User,
		"password": conn.Password,
		"db":       conn.DB,
	})
	if err != nil {
		return Rendering{}, fmt.Errorf("dbbrowser: render adminer connection material: %w", err)
	}
	return Rendering{
		Command: []string{
			"php", "-S", "0.0.0.0:8080",
			"-t", "/var/www/html",
			"/run/secrets/router.php",
		},
		Files: map[string][]byte{
			"router.php":   AdminerRouterPHP(),
			"db-conn.json": connJSON,
		},
	}, nil
}
