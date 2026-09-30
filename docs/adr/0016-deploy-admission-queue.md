# 部署 admission 队列替代并发 409

场景 9 原判"同 App 并发 Deployment 409"对无人值守的 webhook 自动部署是断链（push 触发撞上 in-flight 部署即静默丢失）。决定：同 App 部署请求走 admission 队列——同幂等键/同 commit 去重（返回既有 Deployment）；默认 latest-wins（排队中旧请求被新请求合并）；显式 supersede 抢占在途部署；per-节点并发上限与 queue 满反馈（queue_full）显式返回；排队与在途均可取消。409 收窄为幂等键冲突与互斥资源锁。语义综合 dokploy 内存队列（per-server 分区 + 同服务 FIFO + 可配并发）与 coolify admission（去重/queue_full/cancel），见竞品报告 §3。
