# activator 是唤醒组件的发布镜像（ghcr.io/fleetlyrun/activator；manageds
# activatorDecl 钉版消费）。构建方式二选一：
#
#   1. 本脚本内多阶段构建（buildx，双架构）：
#      docker buildx build --platform linux/amd64,linux/arm64 \
#        -t ghcr.io/fleetlyrun/activator:0.1.0 --push .
#   2. 宿主交叉编译 + 二进制注入（离线确定性，staging 实录形态）：
#      CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o activator ./cmd/activator
#      docker build -f deploy/activator/Dockerfile -t ghcr.io/fleetlyrun/activator:0.1.0 .
#      （BuildArg 二进制注入时用 --build-arg BINARY=activator + 同目录放置）
#
# 底镜像 alpine:3（与 e2e/dind-functions.sh 的测试形态同源）；二进制静态
# 链接（CGO_ENABLED=0），无 shell 依赖。探针面：fleetlyd 侧经 manageds
# Healthcheck tcp 探 8090（activator Listen 口，cmd/activator 缺省）。
FROM alpine:3
ARG BINARY=activator
COPY ${BINARY} /activator
EXPOSE 8090
ENTRYPOINT ["/activator"]
