-- state_backups 上传轨列（E3 对象存储专项设计 §2.3/D-S3-4，迁移 00012，
-- 唯一 DB 迁移，只加纪律）：备份台账增上传三列——
--   upload_status：none（未上传/未配置 S3，合法态不算失败）| ok | failed；
--   uploaded_at  ：最近一次上传尝试的完成时刻（UnixNano；ok/failed 都记
--                  ——failed 行的操作者同样需要知道尝试时点，错误详情在
--                  upload_error）；
--   upload_error ：上传失败原因摘要（截断上界见 state 层写入函数；不含
--                  secret——restic env 的凭证值禁止进台账/事件/日志）。
--
-- 本地备份核一字不动（VACUUM INTO → 回读校验 → manifest → 台账的信任闭
-- 环不改写）：上传是 verify 之后的追加步，既有行的 upload_status 全部
-- 缺省 'none'，行为逐字节不变（架构 §2.8 契约版本化纪律：迁移只加法）。
--
-- Down 仅供 goose 演练（同 00011 先例）；生产回滚 = 恢复快照。

-- +goose Up
ALTER TABLE state_backups ADD COLUMN upload_status TEXT NOT NULL DEFAULT 'none';
ALTER TABLE state_backups ADD COLUMN uploaded_at INTEGER;
ALTER TABLE state_backups ADD COLUMN upload_error TEXT;

-- +goose Down
ALTER TABLE state_backups DROP COLUMN upload_status;
ALTER TABLE state_backups DROP COLUMN uploaded_at;
ALTER TABLE state_backups DROP COLUMN upload_error;
