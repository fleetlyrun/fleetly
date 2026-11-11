// Package api 是 engine 对输入面（gRPC/CLI/assembly）暴露的 DTO、哨兵与
// 输入面原语契约包：受理请求/判定附注/预览与拆除载体、错误哨兵、事件名与
// 载荷构造在此单源。
//
// 依赖方向：本包只依赖 domain/genproto、application/governance 与
// engine/contract 契约叶子，不依赖 engine 根包——engine 与 interfaces 同向
// 消费本包（engine 侧经别名/声明委托零漂移），输入面无需 import 引擎实现。
package api
