// Package execrun 是平台对外部子进程的统一生命周期封装（MG-1：外部
// 进程/会话生命周期统一封装——架构评审结论：逐点修留漏网即缺机制）。
//
// 机制动机：Go 标准库 exec 的 cmd.Run 在「孙进程持有 stdout/stderr 管道
// 写端」时会永久阻塞——cmd.Wait 要等 I/O 拷贝 goroutine 结束，而拷贝要等
// 管道全部写端关闭；直接子进程死了、孙进程（git→ssh、docker exec→容器
// 进程等）仍持有写端时，Wait 永不返回。这不是理论形态：E6（S19）已实证
// SSH git 路径的通道失联挂死并打了 WaitDelay 补丁；本批 H3 又确认 webhook
// 拉源路径的 git fetch 同样裸奔（裸 exec.CommandContext 无 WaitDelay）。
// 机制化的结论：平台拉起的一切外部子进程必须经同一封装治理，不再逐点
// 补丁。
//
// 兜底手段（双保险，缺一不可）：
//   - cmd.WaitDelay（Go 1.20+）：ctx 取消杀进程后，若管道写端仍被孙进程
//     持有，Wait 至多再等 WaitDelay 即强制关闭管道并返回——I/O 挂死下
//     Wait 限时回收的唯一跨平台兜底；
//   - 进程组杀（unix）：Setpgid 让子进程自成进程组，ctx 取消时以
//     syscall.Kill(-pgid, SIGKILL) 组杀——把「只杀直接子进程、孙进程存活」
//     的漏杀面在源头收掉。目标部署形态是 Linux（组杀全程生效）；Windows
//     无跨平台进程组原语，仅 WaitDelay 兜底。
//
// 契约：Run 的阻塞上界 = 调用方 ctx 自身预算 + WaitDelay——调用方永不因
// 子进程/孙进程的 I/O 挂死而无限等待。
package execrun
