// Package logcursor 是采集游标聚合的领域面（F2.4，ADR-0040 决策 2，
// ADR-0075 Repo 接口层）：每活跃隔离域一行，last_ts 只在 Ingest 成功后
// 推进（断流自愈锚——Since=游标从 docker json-file 缓冲重放补窗）。
// 无行类型（游标是 namespace → time.Time 的单列事实）；Repo 接口在此
// 单源，SQLite 实现住 infrastructure/persistence/logcursor。
package logcursor
