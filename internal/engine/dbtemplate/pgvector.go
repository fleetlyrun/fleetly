package dbtemplate

// pgvector 模板：postgres 基座 + pgvector 扩展初始化覆盖。钉版精确版本
// （与 postgres 模板同 PG major/Debian suite——0.8.6-pg17-bookworm）。

// pgvectorTemplate 嵌入 postgresTemplate 复用基座面（探针/材料/连接串/
// 卷目标与 postgres 逐字一致），只覆盖身份与 Workload。
type pgvectorTemplate struct {
	postgresTemplate
}

func (pgvectorTemplate) Engine() string { return "pgvector" }
func (pgvectorTemplate) Meta() Info {
	return Info{Version: "0.8.6-pg17-bookworm", Port: 5432}
}

// pgvectorImageTag / pgvectorImageDigest 是 digest 钉定对（F2.7/ADR-0045；
// bump 纪律同 postgresImageTag 注）。digest 取 2026-10-05 Docker Hub index
// digest（multi-arch 真源）。
const (
	pgvectorImageTag    = "pgvector/pgvector:0.8.6-pg17-bookworm"
	pgvectorImageDigest = "sha256:cf134a767f474095eeba57e0117be8e568e011a63f33fbf252f14c9b760f8e6f"
)

func (pgvectorTemplate) Image() string       { return pinnedRef(pgvectorImageTag, pgvectorImageDigest) }
func (pgvectorTemplate) ImageDigest() string { return pgvectorImageDigest }

func (pgvectorTemplate) Workload() (map[string]string, []string) {
	env := map[string]string{
		"POSTGRES_USER":          pgUser,
		"POSTGRES_DB":            pgDBName,
		"POSTGRES_PASSWORD_FILE": "/run/secrets/" + PasswordFile,
		"PGDATA":                 pgDataDir, // 与 postgres 基座同值：避开镜像 VOLUME 遮蔽（见 postgres.go pgDataDir 注）
	}
	// pgvector 扩展须在首启初始化期建（postgres 镜像的
	// docker-entrypoint-initdb.d 机制；argv 不含任何凭证值）。
	command := []string{"bash", "-c",
		"mkdir -p /docker-entrypoint-initdb.d && " +
			"printf 'CREATE EXTENSION IF NOT EXISTS vector;\\n' > /docker-entrypoint-initdb.d/01-vector.sql && " +
			"exec docker-entrypoint.sh postgres"}
	return env, command
}
