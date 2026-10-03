// Package dbtemplate 是数据库模板注册表（ADR-0029 决策 2）：engine 名 →
// 钉版镜像 + 端口 + 数据卷目标 + 凭证材料渲染方式。模板参数创建即不可变
// （版本矩阵随 F2）；钉版口径 ADR-0021（zot 先例）。
//
// 形状（2026-10-03，架构评审第二轮候选 1，F2.7 目录化提前落）：per-engine
// 接口 adapter——每引擎一个类型实现 Template，注册表零 switch（原四把
// per-engine switch 收编进 entry 本身；加引擎 = 加一个类型 + 一个注册表
// entry，F2.7 只剩 digest 钉定门禁）。host 注入：模板只拥有"这个引擎的
// URL 长什么样"，铸名公式（db-<id>）留在 engine（TaskDNSName 先例同款
// 分居）。本包 stdlib-only 近叶子（import 守卫口径下无 internal 依赖）。
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
	// Image 返回钉版镜像引用（ADR-0021 口径；digest 钉定随 F2.7）。
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
	// 是 Secret 里的连接串，ADR-0029 决策 6）。
	Materials(password string) map[string][]byte
	// ConnURL 铸连接串（host 由调用方注入——铸名公式真源在 engine）。
	ConnURL(host, password string) string
	// BackupCommand 是备份执行链的引擎命令（F2.2 预留空槽，ADR-0029 决策
	// 7：执行链落地前恒空串；接口一次定形，届时只填实现）。
	BackupCommand() string
	// ImageDigest 是镜像 digest 钉定面（F2.7 预留空槽：空串 = 未钉，现状
	// tag 级钉定；门禁随 F2.7 落）。
	ImageDigest() string
}

// registry 是值域注册表（受理位校验与投影共用的单源；实现集即值域）。
var registry = map[string]Template{
	"postgres": postgresTemplate{},
	"pgvector": pgvectorTemplate{},
	"redis":    redisTemplate{},
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
