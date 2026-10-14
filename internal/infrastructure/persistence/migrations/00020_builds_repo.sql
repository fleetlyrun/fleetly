-- +goose Up
-- builds.repo：构建产物所在的仓库名（不含 registry 地址；ADR-0036 N2 兑现
-- 节 2）。仓布局切前缀（<projectID>/<appID>）后，digest 引用在投影期组合
-- 需要知道产物落在哪个仓库：新构建落前缀仓；存量行空值 = 扁平回退
-- （<appID>——Revision 冻结 digest 的兼容面，扁平仓保持全用户可读）。
-- repo 在 Build 行创建时定型（不可变——与 digest 同为产物定位事实）。
ALTER TABLE builds ADD COLUMN repo TEXT NOT NULL DEFAULT '';

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
