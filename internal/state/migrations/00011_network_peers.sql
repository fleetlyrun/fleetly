-- +goose Up
-- 跨 Project peer 声明面（ADR-0013 附录 A.1，F1.8）：一行 = 一条
-- (network, peer project) 挂靠声明，状态机 pending → approved → revoked。
-- 撤销后可重新声明（新审批环）——唯一性只在非 revoked 行间成立。
-- network_project_id 不落本表（经 network 行解析，无反范式副本）。
CREATE TABLE network_peers (
  id              TEXT PRIMARY KEY,
  network_id      TEXT NOT NULL,
  peer_project_id TEXT NOT NULL,
  state           TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  approved_at     TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_network_peers_active ON network_peers(network_id, peer_project_id) WHERE state != 'revoked';
CREATE INDEX idx_network_peers_peer ON network_peers(peer_project_id);

-- +goose Down
-- goose 只前滚（架构 §8）。
