# 探针应用镜像定义（T2.23 升级 E2E）：scratch + 静态二进制，零 registry
# 依赖。宿主侧构建并经 docker save | gzip 注入 dind（docker load），
# 避免 dind 内 buildkit 形态差异：
#   docker build --platform linux/amd64 -t probeapp:1 deploy/testdata/probeapp
FROM scratch
COPY probe /probe
ENTRYPOINT ["/probe"]
