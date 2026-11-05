package dbtemplate

// pgvector 模板：postgres 基座 + pgvector 扩展初始化覆盖。B2a 版本化
//（ADR-0094 决策 7）：在册 0.8-pg17/0.8-pg18（vector minor 平台钉版 0.8，
// 复合短名表达二维矩阵——vector × pg major 各自独立，见 postgres.go
// pgvectorVersions 注），0.8-pg18 Default；tag 精确版 0.8.7（2026-11-05
// 上游最新稳定 0.8.x，digest 随 matrix 实测钉定）。

import (
	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// pgvectorTemplate 嵌入 postgresTemplate 复用基座面（探针/材料/连接串/
// 卷目标/PGDATA 分叉与 postgres 逐字一致），只覆盖身份、版本矩阵、镜像
// 仓库与 Workload。
type pgvectorTemplate struct {
	postgresTemplate
}

func (pgvectorTemplate) Engine() string { return "pgvector" }
func (pgvectorTemplate) Versions() []enginetemplate.VersionSpec {
	return pgVersionsToSpecs(pgvectorVersions)
}
func (t pgvectorTemplate) bindVersion(spec enginetemplate.VersionSpec) Template {
	return pgvectorTemplate{postgresTemplate{version: pgVersionOf(pgvectorVersions, spec)}}
}
func (t pgvectorTemplate) Image() string {
	return pinnedRef(pgvectorRepo+":"+t.version.Tag, t.version.Digest)
}

func (t pgvectorTemplate) Workload() (map[string]string, []string) {
	// env 与 postgres 基座同源（pgWorkloadEnv——PGDATA 绑版分叉含在内：
	// 0.8-pg17 显式 pgdata 子目录、0.8-pg18 走镜像默认，见 postgres.go
	// pgDataDir 注）。
	command := []string{"bash", "-c",
		"mkdir -p /docker-entrypoint-initdb.d && " +
			"printf 'CREATE EXTENSION IF NOT EXISTS vector;\\n' > /docker-entrypoint-initdb.d/01-vector.sql && " +
			"exec docker-entrypoint.sh postgres"}
	return pgWorkloadEnv(t.version), command
}
