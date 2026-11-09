// 合成锚具（maintenanceguard_test.go 判据泛化的应用模块正向锚，ADR-0088
// P4 wave-0）。本文件在 testdata/ 下不参与编译（go 工具忽略 testdata），
// 只被契约测试的 AST 扫描器消费。
//
// 应用模块形 struct 自持 capability.Runtime 型字段（wave-1 接收者迁移后的
// 形态——接收者名 a、字段名 rt，与 Engine/e/runtime 全不同；旧字段名
// 字面判据在此静默致盲）。三方法覆盖类型化判据的三条腿：
//   - lockedThenWrite  先锁后写 = 绿；
//   - bareWrite        不锁 = 红；
//   - lockAfterWrite   锁在写之后 = 红（token 序判据）。
package engine

// satelliteAnchor 形似 wave-1 应用模块 struct 的最小样本：Runtime 型字段直持
// （类型索引第一遍收编，字段名不必叫 runtime）。
type satelliteAnchor struct {
	rt capability.Runtime
}

func (a *satelliteAnchor) lockedThenWrite(ctx context.Context) {
	unlock := lockMaintenance()
	defer unlock()
	_ = a.rt.Ensure(ctx, ns, ws, gen, materials)
}

func (a *satelliteAnchor) bareWrite(ctx context.Context) {
	_ = a.rt.Remove(ctx, ns)
}

func (a *satelliteAnchor) lockAfterWrite(ctx context.Context) {
	_ = a.rt.Remove(ctx, ns)
	unlock := lockMaintenance()
	_ = unlock
}
