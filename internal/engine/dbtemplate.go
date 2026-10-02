package engine

// 数据库模板注册表（ADR-0029 决策 2）：engine 名 → 钉版镜像 + 端口 +
// 数据卷目标 + 凭证材料渲染方式。模板参数创建即不可变（版本矩阵随 F2）；
// 钉版口径 ADR-0021（zot 先例）。
//
// 钉版调研（2026-10-02）：postgres 取 major+suite 级钉（trixie 基座启动
// 坑 docker-library/postgres#1363 规避；精确 patch 钉随 F2）；pgvector 落
// 上游 pgvector/pgvector（percona/pgvector 镜像不存在于 Docker Hub——
// Percona 把 pgvector 打进其发行镜像，单 arch 且约 700MB，不取）。

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// dbTemplate 是一个数据库模板的声明。
type dbTemplate struct {
	engine  string
	image   string // 钉版镜像（ADR-0021 口径）
	version string // 面向回显的版本描述（模板钉版的短名）
	port    int32  // 服务端口（连接串与探针共用）
	// dataTarget 是数据卷容器内挂载目标。
	dataTarget string
	// urlScheme 是连接串 scheme（postgresql / redis）。
	urlScheme string
	// user / dbName 是 postgres 系的连接账号与库名（redis 留空）。
	user   string
	dbName string
}

// dbTemplates 是值域注册表（受理位校验与投影共用的单源）。
var dbTemplates = map[string]dbTemplate{
	"postgres": {
		engine: "postgres", image: "postgres:17-bookworm", version: "17-bookworm",
		port: 5432, dataTarget: "/var/lib/postgresql/data",
		urlScheme: "postgresql", user: "fleetly", dbName: "fleetly",
	},
	"pgvector": {
		engine: "pgvector", image: "pgvector/pgvector:0.8.6-pg17-bookworm", version: "0.8.6-pg17-bookworm",
		port: 5432, dataTarget: "/var/lib/postgresql/data",
		urlScheme: "postgresql", user: "fleetly", dbName: "fleetly",
	},
	// redis 的 requirepass 不支持密码文件：平台合成 redis.conf 经材料
	// 通道注入（argv 只含配置路径，不落明文——ADR-0014 材料纪律）。
	"redis": {
		engine: "redis", image: "redis:7.4", version: "7.4",
		port: 6379, dataTarget: "/data",
		urlScheme: "redis",
	},
}

// DBEngines 返回在册模板的 engine 值域（受理位校验消费；排序稳定）。
func DBEngines() []string {
	out := make([]string, 0, len(dbTemplates))
	for k := range dbTemplates {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DatabaseEngineInfo 是 API 回显面的模板信息（version/port；不含镜像——
// 能力自描述面由 Registry/Provider 承载，数据库模板不是 Provider）。
type DatabaseEngineInfo struct {
	Version string
	Port    int32
}

// DatabaseEngineInfoFor 返回 engine 的回显信息（单源：模板注册表）。
func DatabaseEngineInfoFor(engine string) (DatabaseEngineInfo, bool) {
	tpl, ok := dbTemplates[engine]
	if !ok {
		return DatabaseEngineInfo{}, false
	}
	return DatabaseEngineInfo{Version: tpl.version, Port: tpl.port}, true
}

// dbTemplateFor 返回 engine 对应模板。
func dbTemplateFor(engine string) (dbTemplate, bool) {
	tpl, ok := dbTemplates[engine]
	return tpl, ok
}

// 凭证材料文件名（容器内 /run/secrets/<名>；ADR-0029 决策 6）。
const (
	dbPasswordFile   = "database-password"   // postgres 系：密码文件（POSTGRES_PASSWORD_FILE 指向）
	dbRedisConfFile  = "database-redis-conf" // redis：平台合成的 requirepass 配置
	dbConnectTimeout = 10 * time.Second      // 探针节拍（启动宽限给足首次初始化）
)

// workloadRender 渲染模板的 Workload 侧参数（env/command——材料文件名是
// 路径不是值，env 不落明文，ADR-0014）。
func (t dbTemplate) workloadRender() (env map[string]string, command []string) {
	switch t.engine {
	case "postgres", "pgvector":
		env = map[string]string{
			"POSTGRES_USER":          t.user,
			"POSTGRES_DB":            t.dbName,
			"POSTGRES_PASSWORD_FILE": "/run/secrets/" + dbPasswordFile,
		}
		if t.engine == "pgvector" {
			// pgvector 扩展须在首启初始化期建（postgres 镜像的
			// docker-entrypoint-initdb.d 机制；argv 不含任何凭证值）。
			command = []string{"bash", "-c",
				"mkdir -p /docker-entrypoint-initdb.d && " +
					"printf 'CREATE EXTENSION IF NOT EXISTS vector;\\n' > /docker-entrypoint-initdb.d/01-vector.sql && " +
					"exec docker-entrypoint.sh postgres"}
		}
		return env, command
	case "redis":
		return nil, []string{"redis-server", "/run/secrets/" + dbRedisConfFile}
	default:
		return nil, nil // 不可达：值域由注册表封闭
	}
}

// materialsRender 渲染 DB Workload 的凭证材料（密码经 URL 解析取得——
// 单真源是 Secret 里的连接串，ADR-0029 决策 6）。
func (t dbTemplate) materialsRender(password string) map[string][]byte {
	switch t.engine {
	case "postgres", "pgvector":
		return map[string][]byte{dbPasswordFile: []byte(password)}
	case "redis":
		// appendonly：容器重启不丢已确认写（数据卷已在；AOF 是库级持久
		// 语义的最小诚实形态）。
		return map[string][]byte{dbRedisConfFile: []byte("requirepass " + password + "\nappendonly yes\n")}
	default:
		return nil
	}
}

// DatabaseConnectionURL 铸连接串（API 创建面消费的单源公式；host 是
// 网内 DNS 名 db-<id>——只在项目网内可解析，这是诚实的连接面）。
func DatabaseConnectionURL(engine, databaseID, password string) (string, error) {
	tpl, ok := dbTemplateFor(engine)
	if !ok {
		return "", fmt.Errorf("engine: unknown database engine %q", engine)
	}
	host := DatabaseDNSName(databaseID)
	switch tpl.engine {
	case "postgres", "pgvector":
		u := &url.URL{
			Scheme: tpl.urlScheme,
			User:   url.UserPassword(tpl.user, password),
			Host:   fmt.Sprintf("%s:%d", host, tpl.port),
			Path:   tpl.dbName,
		}
		return u.String(), nil
	case "redis":
		u := &url.URL{
			Scheme: tpl.urlScheme,
			User:   url.UserPassword("", password),
			Host:   fmt.Sprintf("%s:%d", host, tpl.port),
			Path:   "0",
		}
		return u.String(), nil
	default:
		return "", fmt.Errorf("engine: unknown database engine %q", engine)
	}
}

// dbPasswordFromURL 从连接串提取密码（材料渲染侧的单真源回读）。
func dbPasswordFromURL(connectURL string) (string, error) {
	u, err := url.Parse(connectURL)
	if err != nil {
		return "", fmt.Errorf("parse database connection url: %w", err)
	}
	if u.User == nil {
		return "", fmt.Errorf("database connection url carries no credentials")
	}
	password, _ := u.User.Password()
	if password == "" {
		return "", fmt.Errorf("database connection url carries no password")
	}
	return password, nil
}

// DatabaseDNSName 铸 per-Database 稳定 DNS 名（engine 铸名公式真源，
// TaskDNSName 先例；Provider 经 Addressing 声明映射为自己的原语）。
func DatabaseDNSName(databaseID string) string {
	return "db-" + strings.ToLower(databaseID)
}
