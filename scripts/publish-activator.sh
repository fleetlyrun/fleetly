#!/bin/sh
# publish-activator 构建+发布 activator 镜像到 ghcr（多架构 buildx；单架构
# 回退形态见 deploy/activator/Dockerfile 头注）。版本 = manageds
# activatorDecl 钉版（0.1.0）；升版须同 commit 改 internal/application/
# manageds/managed.go 的 ActivatorImage。
#
# 前置：ghcr 登录（gh auth token | docker login ghcr.io -u <user> -p-）+ buildx。
set -eu
VERSION="${1:-0.1.0}"
IMAGE="ghcr.io/fleetlyrun/activator"

# 双架构交叉编译（CGO=0 静态链接——alpine 运行时零 loader 依赖）。
for ARCH in amd64 arm64; do
  mkdir -p ".build/activator-$ARCH"
  CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -o ".build/activator-$ARCH/activator" ./cmd/activator
done

# buildx 多架构镜像（push 直推；本地 load 形态 --load 单架构自测用）。
docker buildx build --platform linux/amd64,linux/arm64 \
  -f deploy/activator/Dockerfile.ci \
  -t "$IMAGE:$VERSION" -t "$IMAGE:latest" --push .
echo "published $IMAGE:$VERSION (+latest)"
