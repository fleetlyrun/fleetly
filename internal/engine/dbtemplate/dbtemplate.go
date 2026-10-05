// Package dbtemplate 是数据库模板注册表（ADR-0029 决策 2）：engine 名 →
// 钉版镜像 + 端口 + 数据卷目标 + 凭证材料渲染方式。模板参数创建即不可变
// （版本矩阵随 F2）；钉版口径 ADR-0021（zot 先例）。
//
// 形状（2026-10-03，架构评审第二轮候选 1，F2.7 目录化提前落）：per-engine
// 接口 adapter——每引擎一个类型实现 Template，注册表零 switch（原四把
// per-engine switch 收编进 entry 本身；加引擎 = 加一个类型 + 一个注册表
// entry）。F2.7 收口（2026-10-05，ADR-0045）：镜像引用 digest 钉定门禁
// 落地——Image() 恒为 tag@digest 双段形态，钉定对常量住各 adapter（清单
// 视图 = dbtemplate_test 的 TestTemplateFaces 钉板）。host 注入：模板只拥有
// "这个引擎的 URL 长什么样"，铸名公式（db-<id>）留在 engine（TaskDNSName
// 先例同款分居）。本包 stdlib-only 近叶子（import 守卫口径下无 internal
// 依赖）。
//
// 钉版调研（2026-10-02）：postgres 取 major+suite 级钉（trixie 基座启动
// 坑 docker-library/postgres#1363 规避；精确 patch 钉随 F2）；pgvector 落
// 上游 pgvector/pgvector（percona/pgvector 镜像不存在于 Docker Hub——
// Percona 把 pgvector 打进其发行镜像，单 arch 且约 700MB，不取）。
package dbtemplate

import (
	"fmt"
	"net/url"
	"sort"
)

// Info 是 API 回显面的模板信息（version/port；不含镜像——能力自描述面由
// Registry/Provider 承载，数据库模板不是 Provider，ADR-0029）。
type Info struct {
	Version string
	Port    int32
}

// Template 是一个数据库引擎的完整模板知识（per-engine adapter 的接口面；
// 注册表值域即实现集）。
type Template interface {
	// Engine 返回引擎名（值域键；Workload.Process 同名）。
	Engine() string
	// Meta 返回回显与 spec 组装共用的版本/端口（单源）。
	Meta() Info
	// Image 返回钉版镜像引用（ADR-0021 口径；F2.7/ADR-0045 起 digest 钉定：
	// 恒为 tag@sha256:<index digest> 双段形态，digest 主导、tag 是可读性面）。
	Image() string
	// DataTarget 返回数据卷容器内挂载目标。
	DataTarget() string
	// Workload 渲染 Workload 侧参数（env/command——材料文件名是路径不是
	// 值，env 不落明文，ADR-0014）。
	Workload() (env map[string]string, command []string)
	// Probe 渲染引擎原生存活探针（exec 形态；镜像必带引擎自带客户端工具，
	// 不依赖通用 TCP 探针方言的 nc 假设——staging 真机实证 pgvector 镜像
	// 无 nc，F1.15）。
	Probe() []string
	// Materials 渲染 DB Workload 的凭证材料（密码经 URL 解析取得——单真源
	// 是 Secret 里的连接串，ADR-0029 决策 6）。密码超出安全字符集返回
	// 错误（渲染面 fail-closed，见 validatePassword）。
	Materials(password string) (map[string][]byte, error)
	// ConnURL 铸连接串（host 由调用方注入——铸名公式真源在 engine）。
	ConnURL(host, password string) string
	// Backup 渲染备份执行（ADR-0039 决策 5）：完整 argv + 凭证材料 +
	// env；dump 产物走工具容器 stdout（执行器流送 ObjectStore）。host 是
	// engine 铸的网内 DNS 名（db-<id>）。
	Backup(host, password string) (BackupSpec, error)
	// Restore 渲染恢复执行：流式（stdin 注入运行中的库）或预置卷（redis
	// 形态——RDB 仅启动时装载，ADR-0039 决策 5）。
	Restore(host, password string) (RestoreSpec, error)
	// ImageDigest 是镜像 digest 钉定面（F2.7/ADR-0045 落地）：带算法前缀的
	// index digest，与 Image() 的 digest 段恒一致（P7 第三消费点 = registry
	// digest 透传，只核对传递完整性——测试门禁断言自洽）。
	ImageDigest() string
}

// pinnedRef 组装 digest 钉定引用（F2.7/ADR-0045）：ref = tag@digest 双段
// 形态的唯一拼装点——digest 主导（编排器按内容寻址拉取），tag 是可读性面
// （载体/事件流里版本可见）。钉的是 OCI index（manifest-list）digest——
// multi-arch 真源，registry API Docker-Content-Digest 头原值。
func pinnedRef(tag, digest string) string { return tag + "@" + digest }

// BackupSpec 是一次备份的引擎渲染产物（argv 纯数组、零 shell 拼串——args
// 数组文化，shellguard 射程不变）。执行器（engine 备份环）把 SecretFiles
// 落为工具容器的 /run/secrets/<名>（与 DB Workload 材料同语义），stdout
// 流式写 ObjectStore（digest 由 Put 流式铸造）。
type BackupSpec struct {
	Argv []string
	// Env 只承载路径引用类值；唯一登记例外 = redis REDISCLI_AUTH（redis-cli
	// 无文件面，ADR-0039 决策 5 例外注）。
	Env         map[string]string
	SecretFiles map[string][]byte
}

// RestoreMode 是恢复执行形态。
type RestoreMode string

const (
	// RestoreStream：dump 经工具容器 stdin 注入运行中的目标库
	// （pg_restore / mysql / mongorestore）。
	RestoreStream RestoreMode = "stream"
	// RestorePreseed：dump 经工具容器 stdin 落为数据卷内文件（redis RDB
	// 仅启动装载——恢复发生在库首启前；执行器把库的平台卷挂到
	// SeedMountPoint 后执行 Argv）。
	RestorePreseed RestoreMode = "preseed"
)

// SeedMountPoint 是预置卷形态下工具容器挂载数据卷的容器内固定挂点
// （模板 argv 与 engine 执行器共用的单源常量）。
const SeedMountPoint = "/seed"

// BackupInputPath 是恢复流的输入文件容器内固定挂载路径（ADR-0039 落地
// 实录：hijack attach 的 CloseWrite 不向容器 stdin 送 EOF——dind 实证
// mysql 客户端读流永挂；恢复流经文件挂载承载，模板 argv 与执行器共用
// 本单源）。
const BackupInputPath = "/backup-input"

// RestoreSpec 是一次恢复的引擎渲染产物。
type RestoreSpec struct {
	Mode RestoreMode
	// Argv：stream 形态 = 恢复客户端（stdin = dump）；preseed 形态 =
	// 落盘命令（stdin = dump，写进 SeedMountPoint 下的卷内路径）。
	Argv        []string
	Env         map[string]string
	SecretFiles map[string][]byte
}

// validatePassword 是渲染面的共享安全闸：密码只准 [0-9A-Za-z]。动机是
// 引擎渲染面的并集约束——pgpass 行格式冒号不可转义、redis.conf 行语法
// 空白即断、mongosh init 脚本单引号串、mysql INI 换行即注入；平台铸造
// 公式 = hex 48（crypto/rand），用户面无覆写通道（PutSecret 拒 database:
// 保留前缀），本闸是纵深防御而非受理位（hostile 值到不了这里也照样拒）。
func validatePassword(password string) error {
	if password == "" {
		return fmt.Errorf("dbtemplate: password must not be empty")
	}
	for i := 0; i < len(password); i++ {
		c := password[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			continue
		}
		return fmt.Errorf("dbtemplate: password carries characters outside the render-safe set [0-9A-Za-z]")
	}
	return nil
}

// registry 是值域注册表（受理位校验与投影共用的单源；实现集即值域）。
var registry = map[string]Template{
	"postgres": postgresTemplate{},
	"pgvector": pgvectorTemplate{},
	"redis":    redisTemplate{},
	"mysql":    mysqlTemplate{},
	"mongo":    mongoTemplate{},
}

// Engines 返回在册引擎值域（受理位校验消费；排序稳定）。
func Engines() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// For 返回 engine 对应模板（不存在 ok=false；受理位先校验值域）。
func For(engine string) (Template, bool) {
	tpl, ok := registry[engine]
	return tpl, ok
}

// InfoFor 返回 engine 的回显信息（单源：模板注册表）。
func InfoFor(engine string) (Info, bool) {
	tpl, ok := registry[engine]
	if !ok {
		return Info{}, false
	}
	return tpl.Meta(), true
}

// PasswordFromURL 从连接串提取密码（模板 URL 契约的读回面——写读同源：
// ConnURL 铸的形状在此解析；材料渲染侧的单真源回读）。
func PasswordFromURL(connectURL string) (string, error) {
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
