# 构建执行面 v1：恒在控制面节点

Build 恒在控制面节点执行（诚实单点口径的自然延伸）：BuildKit + 本机 daemon，并发上限可配（队列 admission 同 ADR-0016），构建缓存只在本机。多节点构建缓存分发与独立构建节点（dokploy/coolify 的 build server 形态）显式延后，不进 v1 叙事。上传构建（build-from-upload）走流式 tar + 大小上限，断点续传延后。
