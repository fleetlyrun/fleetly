# 源接入三轨：Git webhook 拉源、镜像引用、上传产物；无 push 收包面

> 2026-09-30（ADR-0010）：webhook 单轨为归档裁决，降级为**参考默认**，待重估（git push 部署流在 caprover/tsuru 血统 PaaS 中是经典 DX，重估时认真比较）。

继承归档 ADR-0012 终裁：CD 内置、CI 归 Git 托管方；Git 触发走 webhook + 控制面拉源（生态主流、无收包面维护成本），SSH push 收包整体不做。新增两轨服务 Agent 与 torchwood 场景：镜像引用（直接部署现有 digest）与上传产物（build-from-upload）。bare 仓库作为拉源落点保留。
