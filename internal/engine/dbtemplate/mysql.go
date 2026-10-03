package dbtemplate

// mysql 模板（F2.1 矩阵扩展）：官方镜像入口脚本支持全部敏感变量的 _FILE
// 形态——app 账号密码走文件（argv/env 不落明文，ADR-0014 材料纪律）。
// root 密码与 app 密码同值双文件：Secret 真源是单条连接串（ADR-0029 决策
// 6），root 是容器内管理位、不进任何回显面——同值是单真源约束下的最小
// 诚实形态，隔离增益为零但攻击面不扩大（两文件同卷同权限）。

import (
	"fmt"
	"net/url"
)

// MySQL 凭证材料文件名（容器内 /run/secrets/<名>）。
const (
	MySQLPasswordFile     = "database-password"
	MySQLRootPasswordFile = "database-root-password"
)

// mysqlUser / mysqlDBName 是 mysql 的连接账号与库名（模板冻结值）。
const (
	myUser   = "fleetly"
	myDBName = "fleetly"
)

// mysqlTemplate 是 mysql 引擎的模板 adapter。
type mysqlTemplate struct{}

func (mysqlTemplate) Engine() string     { return "mysql" }
func (mysqlTemplate) Meta() Info         { return Info{Version: "8.4", Port: 3306} }
func (mysqlTemplate) Image() string      { return "mysql:8.4" }
func (mysqlTemplate) DataTarget() string { return "/var/lib/mysql" }

func (mysqlTemplate) Workload() (map[string]string, []string) {
	return map[string]string{
		"MYSQL_DATABASE":           myDBName,
		"MYSQL_USER":               myUser,
		"MYSQL_PASSWORD_FILE":      "/run/secrets/" + MySQLPasswordFile,
		"MYSQL_ROOT_PASSWORD_FILE": "/run/secrets/" + MySQLRootPasswordFile,
	}, nil
}

func (mysqlTemplate) Probe() []string {
	// 探针不带凭证：mysqladmin ping 对"连上但被拒（Access denied）"同样
	// 退出 0——拒绝本身就是服务在服的证明（mysql 文档口径，pg_isready
	// 同语义）。
	return []string{"mysqladmin", "ping", "-h", "127.0.0.1"}
}

func (mysqlTemplate) Materials(password string) map[string][]byte {
	return map[string][]byte{
		MySQLPasswordFile:     []byte(password),
		MySQLRootPasswordFile: []byte(password),
	}
}

func (mysqlTemplate) ConnURL(host, password string) string {
	u := &url.URL{
		Scheme: "mysql",
		User:   url.UserPassword(myUser, password),
		Host:   fmt.Sprintf("%s:%d", host, 3306),
		Path:   myDBName,
	}
	return u.String()
}

func (mysqlTemplate) BackupCommand() string { return "" } // F2.2 空槽
func (mysqlTemplate) ImageDigest() string   { return "" } // F2.7 空槽
