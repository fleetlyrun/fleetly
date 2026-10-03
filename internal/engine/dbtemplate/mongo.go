package dbtemplate

// mongo 模板（F2.1 矩阵扩展）：官方镜像无 _FILE 密码变量——凭证纪律走
// 双材料形态：平台合成 mongod.conf（开 authorization）与 init 脚本（首启
// 建库建用户，密码嵌在脚本材料内——材料即密文通道，ADR-0014）。命令把
// init 脚本拷进 entrypoint 的初始化目录后转交官方入口（固定字面量，零
// 拼接、零变量）。探针用 mongosh ping——驱动拓扑探测面，授权前可用。

import (
	"fmt"
	"net/url"
)

// mongo 凭证材料文件名（容器内 /run/secrets/<名>）。
const (
	MongoConfFile    = "database-mongo-conf"
	MongoInitJSFile  = "database-mongo-init"
	mongoInitDropDir = "/docker-entrypoint-initdb.d"
)

// mongoUser / mongoDBName 是 mongo 的连接账号与库名（模板冻结值；用户建
// 在业务库内——连接串 authSource 缺省即路径库名，零参数自洽）。
const (
	moUser   = "fleetly"
	moDBName = "fleetly"
)

// mongoTemplate 是 mongo 引擎的模板 adapter。
type mongoTemplate struct{}

func (mongoTemplate) Engine() string     { return "mongo" }
func (mongoTemplate) Meta() Info         { return Info{Version: "8.0", Port: 27017} }
func (mongoTemplate) Image() string      { return "mongo:8.0" }
func (mongoTemplate) DataTarget() string { return "/data/db" }

func (mongoTemplate) Workload() (map[string]string, []string) {
	return nil, []string{"sh", "-c",
		"cp /run/secrets/" + MongoInitJSFile + " " + mongoInitDropDir + "/10-fleetly-user.js" +
			" && exec docker-entrypoint.sh mongod --config /run/secrets/" + MongoConfFile}
}

func (mongoTemplate) Probe() []string {
	// ping 是驱动拓扑探测面命令：授权开启下也无须认证（hello/ping 家族
	// 的预认证豁免）——不带凭证即证明服务在服。
	return []string{"mongosh", "--quiet", "--eval", "db.adminCommand('ping')"}
}

func (mongoTemplate) Materials(password string) map[string][]byte {
	conf := "storage:\n  dbPath: /data/db\nnet:\n  port: 27017\nsecurity:\n  authorization: enabled\n"
	// 首启初始化脚本（空数据卷时 entrypoint 以本地无认证模式执行一次）：
	// 建业务库与账号。密码是平台生成的 hex 串（URL/JS 双安全字符集）。
	initJS := "db.getSiblingDB('" + moDBName + "').createUser({user: '" + moUser +
		"', pwd: '" + password + "', roles: [{role: 'readWrite', db: '" + moDBName + "'}]});\n"
	return map[string][]byte{
		MongoConfFile:   []byte(conf),
		MongoInitJSFile: []byte(initJS),
	}
}

func (mongoTemplate) ConnURL(host, password string) string {
	u := &url.URL{
		Scheme: "mongodb",
		User:   url.UserPassword(moUser, password),
		Host:   fmt.Sprintf("%s:%d", host, 27017),
		Path:   moDBName, // authSource 缺省 = 路径库，与 init 脚本建户位置自洽
	}
	return u.String()
}

func (mongoTemplate) BackupCommand() string { return "" } // F2.2 空槽
func (mongoTemplate) ImageDigest() string   { return "" } // F2.7 空槽
