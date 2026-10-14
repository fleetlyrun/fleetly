-- +goose Up
-- Route host 全局唯一化（安全批 P0）：host 是平台级命名空间——唯一索引
-- 只到项目内（idx_routes_project_host_path）时，跨项目同 host+path 双
-- 活跃路由共存，Edge 全量发布产出同名 router 互覆/劫持（traefik 的
-- router/service 是同名 map）。改为 (host, path) 全局活跃唯一，partial
-- 语义与旧索引同款（软删 tombstone 让位）。
DROP INDEX idx_routes_project_host_path;
CREATE UNIQUE INDEX idx_routes_host_path ON routes(host, path) WHERE deleted_at = '';

-- +goose Down
-- goose 只前滚（架构 §8）。
