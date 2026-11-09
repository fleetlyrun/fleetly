// 合成锚具（enginefuncregistry_test.go 判据分辨力锚，防盲锚③）。
// 本文件在 testdata/ 下不参与编译（go 工具忽略 testdata），只被契约测试的
// AST 扫描器消费。三函数覆盖判据的三条路径：
//   - pureVerdict   零 I/O 直接引用（只碰入参）→ 必须命中（正向锚）；
//   - commitAnchor  自身持载体段引用（log）→ 必须不命中（载体段判据）；
//   - ioVerdict     只经 commit* 前缀方法调用触 I/O → 必须不命中
//     （emit*/transit*/commit* 前缀判据）。
//
// 判据失明（全命中不了）、过宽（纯函数也判 I/O）、过窄（什么都判纯）任一
// 漂移都红。
package engine

// anchorEngine 形似 Engine 的最小样本：I/O 载体字段（repos/log 段名入
// engineIOCarrierFields）。clock 端口故意不设——时间经参数传入是 ADR-0087
// 决策 2 的域侧纯度判据，engine 侧 clock 不在载体表（见契约测试头注释）。
type anchorEngine struct {
	repos struct{ ready bool }
	log   struct{ msg string }
}

// pureVerdict 零 I/O 直接引用——判据必须命中（登记表 engineFuncRegistry
// 的在册条目指向它）。
func (a *anchorEngine) pureVerdict(n int) bool { return n > 0 }

// commitAnchor 自身持载体引用（log 段）——判据必须不命中；同时供
// ioVerdict 演练 commit* 前缀方法判据。
func (a *anchorEngine) commitAnchor() { a.log.msg = "anchor commit" }

// ioVerdict 只经 commit* 前缀方法调用触 I/O（无载体段）——判据必须
// 不命中（前缀判据的分辨力锚）。
func (a *anchorEngine) ioVerdict(id string) bool {
	a.commitAnchor()
	return id != ""
}
