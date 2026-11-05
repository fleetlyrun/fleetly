-- +goose Up
-- B2c 存储批（ADR-0094 决策 3 / ADR-0095 决策 1、7）：databases 行落
-- 用户面 major 短名 + 白名单旋钮 + 资源面；backups 行落 engine_version
-- （RestoreCompat 的源版本锚，ADR-0094 决策 5）。
--
-- flag-day 前提（2026-11-04 用户裁决：开发期无向后兼容义务，staging 全量
-- 重建后表空）：version 直接 NOT NULL、无 DEFAULT、不回填——本地 dev 库若
-- 存有旧行，本迁移即报错拒绝（SQLite 对含行表加 NOT NULL 无 DEFAULT 列
-- 失败是预期信号），按前提裁定重建库，不写兼容层。新行恒带值（受理位
-- 空 version = Default 解析后落行）。
--
-- settings 是旋钮键值 JSON（dbtemplate 白名单值域，受理位
-- enginetemplate.ValidateSettings 单源执法）；缺省 '{}' = 无旋钮。
-- cpu_millis / memory_mb 可空（NULL = 无限制 = 现状，ADR-0095 决策 7）。
-- backups.engine_version 空 = 旧行「版本未记录」语义（RestoreCompat 对空
-- 源放行但受理文案标注降级风险自负，design §2.4 红队 A10）。
ALTER TABLE databases ADD COLUMN version TEXT NOT NULL;
ALTER TABLE databases ADD COLUMN settings TEXT NOT NULL DEFAULT '{}';
ALTER TABLE databases ADD COLUMN cpu_millis INTEGER;
ALTER TABLE databases ADD COLUMN memory_mb INTEGER;
ALTER TABLE backups ADD COLUMN engine_version TEXT NOT NULL DEFAULT '';

-- +goose Down
-- goose 只前滚（架构 §8：迁移失败 = 恢复 Platform Backup 重放）。
