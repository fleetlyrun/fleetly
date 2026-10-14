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

// mysqlImageTag / mysqlImageDigest 是 digest 钉定对（F2.7/ADR-0045；bump
// 纪律同 postgresImageTag 注）。digest 取 2026-10-05 Docker Hub index
// digest（multi-arch 真源）。
const (
	mysqlImageTag    = "mysql:8.4"
	mysqlImageDigest = "sha256:6ea90827b1100f8f2ae306a539f86d2c264a26ed435a2a9f75551dd5c3aeb242"
)

func (mysqlTemplate) Engine() string      { return "mysql" }
func (mysqlTemplate) Meta() Info          { return Info{Version: "8.4", Port: 3306} }
func (mysqlTemplate) Image() string       { return pinnedRef(mysqlImageTag, mysqlImageDigest) }
func (mysqlTemplate) ImageDigest() string { return mysqlImageDigest }
func (mysqlTemplate) DataTarget() string  { return "/var/lib/mysql" }

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

func (mysqlTemplate) Materials(password string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	return map[string][]byte{
		MySQLPasswordFile:     []byte(password),
		MySQLRootPasswordFile: []byte(password),
	}, nil
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

// myBackupDefaultsFile 是备份/恢复工具的凭证材料文件名（mysql 客户端
// --defaults-extra-file 的 INI 形态；ini 单节断言见 injection 家族测试）。
const myBackupDefaultsFile = "database-backup-defaults"

// myBackupMaterials 渲染 [client] INI（备份/恢复共用；--defaults-extra-file
// 必须是首参——mysql 客户端只读首参位置的 defaults 文件，后置即静默无效）。
func myBackupMaterials(host, password string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	return map[string][]byte{myBackupDefaultsFile: []byte(
		"[client]\nhost=" + host + "\nuser=" + myUser + "\npassword=" + password + "\n")}, nil
}

func (mysqlTemplate) Backup(host, password string) (BackupSpec, error) {
	files, err := myBackupMaterials(host, password)
	if err != nil {
		return BackupSpec{}, err
	}
	// --single-transaction：InnoDB 一致快照不锁表；SQL 文本 dump 走 stdout。
	return BackupSpec{
		Argv: []string{"mysqldump", "--defaults-extra-file=/run/secrets/" + myBackupDefaultsFile,
			"--single-transaction", "--routines", "--triggers", "--events", myDBName},
		SecretFiles: files,
	}, nil
}

func (mysqlTemplate) Restore(host, password string) (RestoreSpec, error) {
	files, err := myBackupMaterials(host, password)
	if err != nil {
		return RestoreSpec{}, err
	}
	// 目标库由模板 env 面（MYSQL_DATABASE）首启建好；SQL 流经文件挂载
	//（source 客户端命令读文件——hijack stdin EOF 不可达，ADR-0039 实录）。
	return RestoreSpec{
		Mode:        RestoreStream,
		Argv:        []string{"mysql", "--defaults-extra-file=/run/secrets/" + myBackupDefaultsFile, "-e", "source " + BackupInputPath, myDBName},
		SecretFiles: files,
	}, nil
}

// RotatePassword 渲染数据面改密（IA v3 二期⑤b）：ALTER USER 平台账号
// （首启 entrypoint 铸的是 'fleetly'@'%'）；认证走 defaults 文件（current）。
// IDENTIFIED BY 单引号插值的转义安全由 validatePassword 字符集闸承担。
// MYSQL_PASSWORD_FILE 只在空卷首启建号生效，改密必须走数据面。
func (mysqlTemplate) RotatePassword(host, current, next string) (RotateSpec, error) {
	if err := validatePassword(current); err != nil {
		return RotateSpec{}, err
	}
	if err := validatePassword(next); err != nil {
		return RotateSpec{}, err
	}
	files, err := myBackupMaterials(host, current)
	if err != nil {
		return RotateSpec{}, err
	}
	return RotateSpec{
		Argv: []string{"mysql", "--defaults-extra-file=/run/secrets/" + myBackupDefaultsFile,
			"-e", "ALTER USER '" + myUser + "'@'%' IDENTIFIED BY '" + next + "'"},
		SecretFiles: files,
	}, nil
}
