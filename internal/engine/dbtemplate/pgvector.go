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
func (pgvectorTemplate) Image() string { return "pgvector/pgvector:0.8.6-pg17-bookworm" }

func (pgvectorTemplate) Workload() (map[string]string, []string) {
	env := map[string]string{
		"POSTGRES_USER":          pgUser,
		"POSTGRES_DB":            pgDBName,
		"POSTGRES_PASSWORD_FILE": "/run/secrets/" + PasswordFile,
	}
	// pgvector 扩展须在首启初始化期建（postgres 镜像的
	// docker-entrypoint-initdb.d 机制；argv 不含任何凭证值）。
	command := []string{"bash", "-c",
		"mkdir -p /docker-entrypoint-initdb.d && " +
			"printf 'CREATE EXTENSION IF NOT EXISTS vector;\\n' > /docker-entrypoint-initdb.d/01-vector.sql && " +
			"exec docker-entrypoint.sh postgres"}
	return env, command
}
