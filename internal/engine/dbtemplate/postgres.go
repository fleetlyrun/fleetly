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

func (postgresTemplate) Engine() string { return "postgres" }
func (postgresTemplate) Meta() Info     { return Info{Version: "17-bookworm", Port: 5432} }
func (postgresTemplate) Image() string  { return "postgres:17-bookworm" }

// DataTarget 挂父目录而非 /var/lib/postgresql/data：2026-10 刷新的
// postgres:17-bookworm 镜像带 18+ 目录布局入口（docker-library/postgres
// #1259）——挂在 data 子路径会被判"unused mount"拒启（dind 实证）；挂
// 父目录对旧/新入口双兼容（旧：initdb 落 <卷>/data；新：落 <卷>/17/docker）。
// 存量旧布局卷（数据在卷根）的迁移 = 重建 + 备份恢复（F2.2 链路，ADR-0029
// "版本升级路径"口径）。
func (postgresTemplate) DataTarget() string { return "/var/lib/postgresql" }

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

func (postgresTemplate) Materials(password string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	return map[string][]byte{PasswordFile: []byte(password)}, nil
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

// pgBackupPassFile 是备份/恢复工具的凭证材料文件名（PGPASSFILE 行格式
// host:port:db:user:password——冒号在该格式中不可转义，字符集闸是唯一
// 防线；与 Workload 的 POSTGRES_PASSWORD_FILE 形态分立，同名同内容会互
// 相污染语义）。
const pgBackupPassFile = "database-backup-pgpass"

// pgBackupMaterials 渲染备份/恢复共用的 pgpass 材料。
func pgBackupMaterials(host, password string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	return map[string][]byte{pgBackupPassFile: []byte(
		fmt.Sprintf("%s:5432:%s:%s:%s\n", host, pgDBName, pgUser, password))}, nil
}

func (postgresTemplate) Backup(host, password string) (BackupSpec, error) {
	files, err := pgBackupMaterials(host, password)
	if err != nil {
		return BackupSpec{}, err
	}
	// custom 格式（-Fc）：压缩 + TOC + 支持非寻位 stdin 恢复（pg_restore
	// 对管道输入整档缓冲）；产物走 stdout。
	return BackupSpec{
		Argv:        []string{"pg_dump", "-h", host, "-p", "5432", "-U", pgUser, "-d", pgDBName, "--format=custom"},
		Env:         map[string]string{"PGPASSFILE": "/run/secrets/" + pgBackupPassFile},
		SecretFiles: files,
	}, nil
}

func (postgresTemplate) Restore(host, password string) (RestoreSpec, error) {
	files, err := pgBackupMaterials(host, password)
	if err != nil {
		return RestoreSpec{}, err
	}
	// 输入经文件挂载（BackupInputPath 单源——hijack stdin EOF 不可达，
	// ADR-0039 落地实录）；custom 格式恢复目标库（模板 env 面首启建库）。
	return RestoreSpec{
		Mode:        RestoreStream,
		Argv:        []string{"pg_restore", "-h", host, "-p", "5432", "-U", pgUser, "-d", pgDBName, "--no-password", BackupInputPath},
		Env:         map[string]string{"PGPASSFILE": "/run/secrets/" + pgBackupPassFile},
		SecretFiles: files,
	}, nil
}

func (postgresTemplate) ImageDigest() string { return "" } // F2.7 空槽
