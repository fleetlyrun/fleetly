# 受管 Provider 以普通 Workload 自宿，全平台只有一份 reconciler

归档项目为 traefik、VictoriaLogs、registry 等六个受管组件各写一套部署器，六份 Docker client、六份循环骨架、两份 Spec→swarm 翻译，实证漂移（静默 return 无日志）。决定：平台自身的能力组件与用户工作负载走同一条 Runtime 通道——Provider 在 `Describe()` 里声明自己的部署 Spec，唯一一份通用 managedprovider reconciler 负责部署、升级、健康与降级上报。

## Consequences

- 新增受管组件 = 写一个 Provider（能力 + 部署声明），零新增部署代码。
- 平台自举顺序（控制面 → Platform Backup → Managed Provider → 用户 Workload）在 reconciler 一处编排。
