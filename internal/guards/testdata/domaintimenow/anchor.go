// 合成锚具（domainpurity 第四执法面 time.Now 调用禁列的分辨力锚）。
// 本文件在 testdata/ 下不参与编译（go 工具忽略 testdata），只被契约测试的
// AST 扫描器消费：anchorNow 直呼 time.Now()——扫描器必须恰好命中它；
// anchorSince 的 time.Since/Duration 运算不在禁列——必须不命中。两者
// 共同证明判据有分辨力（失明/过宽都红）。
package domain

import "time"

// anchorNow 违规模板：时间自取而非参数传入——必须命中（正向锚）。
func anchorNow() time.Time { return time.Now() }

// anchorSince 合法面：时间差与 Duration 运算——必须不命中。
func anchorSince(start time.Time) bool { return time.Since(start) > time.Second }
