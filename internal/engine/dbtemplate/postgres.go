package dbtemplate

// postgres 系模板（postgres / pgvector 共用基座；pgvector 嵌入覆盖差分面）。
// 凭证材料走密码文件：POSTGRES_PASSWORD_FILE 只含路径，argv/env 不落明文
// （ADR-0029 决策 6；ADR-0014 材料纪律）。

import (
	"fmt"
	"net/url"
)

// PasswordFile 是 postgres 系凭证材料文件名（容器内 /run/secrets/<名>）。
const PasswordFile = "database-password"

// postgresTemplate 是 postgres 引擎的模板 adapter。
type postgresTemplate struct{}

func (postgresTemplate) Engine() string     { return "postgres" }
func (postgresTemplate) Meta() Info         { return Info{Version: "17-bookworm", Port: 5432} }
func (postgresTemplate) Image() string      { return "postgres:17-bookworm" }
func (postgresTemplate) DataTarget() string { return "/var/lib/postgresql/data" }

// user / dbName 是 postgres 系的连接账号与库名（模板冻结值）。
const (
	pgUser   = "fleetly"
	pgDBName = "fleetly"
)

func (postgresTemplate) Workload() (map[string]string, []string) {
	return map[string]string{
		"POSTGRES_USER":          pgUser,
		"POSTGRES_DB":            pgDBName,
		"POSTGRES_PASSWORD_FILE": "/run/secrets/" + PasswordFile,
	}, nil
}

func (postgresTemplate) Probe() []string {
	// 探针不带凭证：pg_isready 的成功回复只证明服务在服（连接握手成功），
	// 不要求认证通过。
	return []string{"pg_isready", "-h", "127.0.0.1", "-p", "5432", "-U", pgUser, "-d", pgDBName}
}

func (postgresTemplate) Materials(password string) map[string][]byte {
	return map[string][]byte{PasswordFile: []byte(password)}
}

func (postgresTemplate) ConnURL(host, password string) string {
	u := &url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(pgUser, password),
		Host:   fmt.Sprintf("%s:%d", host, 5432),
		Path:   pgDBName,
	}
	return u.String()
}

func (postgresTemplate) BackupCommand() string { return "" } // F2.2 空槽
func (postgresTemplate) ImageDigest() string   { return "" } // F2.7 空槽
