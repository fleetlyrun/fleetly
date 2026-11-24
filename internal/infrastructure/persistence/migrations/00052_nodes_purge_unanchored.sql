-- +goose Up
-- 节点观测缓存的非法行清理（2026-11-24）：platform_id 为空串的行不是节点。
-- 未锚定载体（平台 ID 尚未铸造写回）曾以空 ID 入表——k3s Provider 自
-- ADR-0070（2026-10-12）起缺铸造闭包注入、节点恒未锚定，全部节点共用这
-- 一行；swarm 在节点刚加入的两次列举之间偶发落一行。同载体的幽灵行抢先
-- 应答载体反查（中继握手绑定、exec 路由），该节点的中继附着自此被拒。
-- 入表路径已在 Provider 视图与引擎对账两处堵住；本迁移清存量。
-- node_relay_credentials 以 platform_id 为键，空键行同属非法。
DELETE FROM node_relay_credentials WHERE platform_id = '';
DELETE FROM nodes WHERE platform_id = '';
