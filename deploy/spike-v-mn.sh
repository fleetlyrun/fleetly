#!/bin/sh
# deploy/spike-v-mn.sh — V-MN 前置门 spike（E1 多节点专项设计 §4.2 前置门）。
#
# 命题（设计原文）：Traefik http provider + 动态配置内联证书
# （certContent/keyContent）组合未经本仓库实测——不通过则 §2.4 证书通道
# 需复议。本脚本在本地 docker 上实测该组合，产出 443 所服证书的指纹级
# 证据与证书轮换生效时延。
#
# 验证矩阵（一套容器拓扑，三段实验）：
#   form-A  设计原文键名：tls.certificates[{certContent,keyContent}]——
#           命中则设计原文逐字成立；
#   form-B  Traefik v3 源码契约（pkg/tls/certificate.go：Certificate 只有
#           certFile/keyFile 两个 FileOrContent 字段；pkg/types/
#           file_or_content.go：字符串先按路径 os.Stat、失败即按内容消费）
#           ——内联 PEM 直接放进 certFile/keyFile；命中则通道成立、键名
#           按 FileOrContent 契约修正；
#   rotate  在 form-B 生效形态上把内联证书换成第二套自签对，断言
#           pollInterval 量级内 443 所服证书指纹切换（轮换时延实测）。
#
# 判定（退出码 0 = 前置门通过）：
#   form-A 命中                 → PASS-LITERAL（§2.4 原文逐字成立）
#   form-A 不中且 form-B + 轮换 → PASS-FILEORCONTENT（通道成立，键名
#                                 修正项回写设计文档 §2.4/E1-2）
#   其余                        → FAIL（§2.4 证书通道需复议；exit 1）
#
# 拓扑（用户桥接网络 <prefix>-net；全部镜像钉 digest，台账见
# docs/runbooks/image-prepull.md）：
#   <prefix>-backend   busybox httpd   路由后端（200 体 spike-ok）
#   <prefix>-config    busybox httpd   动态配置端点（卷内 dynamic.json）
#   <prefix>-traefik   traefik:v3.5 钉版      443 → 宿主 $HOST_PORT
#
# 镜像口径：httpd 容器用官方 busybox:1.36（alpine 的 busybox 不含 httpd
# applet——实测 exec 报 not found；apk add busybox-extras 引入运行期外部
# 依赖与未钉版本，弃）。busybox:1.36 多架构 index digest 2026-09-20 经
# docker buildx imagetools inspect 解析（sha256:73aaf090…b11662）；本阶段
# 文件清单不含 image-prepull.md，台账行增补列入遗留项。
#
# 依赖（宿主）：docker / openssl / curl。可复跑：启动前与退出（含信号）
# 均清理同名资源与临时目录。Windows Git Bash 与 Linux 均可跑（MSYS 参数
# 转换在 MINGW/MSYS/CYGWIN 下显式关闭；注释中文、用户可见输出英文）。
# 宿主端口冲突时换端口：SPIKE_V_MN_PORT=9443 sh deploy/spike-v-mn.sh。

set -u

# ------------------------------------------------------------------ 常量
P='spike-v-mn'
NET="$P-net"
VOL="$P-cfg"
CTR_BACKEND="$P-backend"
CTR_CONFIG="$P-config"
CTR_TRAEFIK="$P-traefik"
TRAEFIK_IMAGE='traefik:v3.5@sha256:16acb89c6db341182970d6fdafece31303b0a380a8ed7aa51682e225229bf1d2'
BUSYBOX_IMAGE='busybox:1.36@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662'
TLS_HOST='spike.test'
HOST_PORT="${SPIKE_V_MN_PORT:-8443}"
PROVIDER_POLL='1s'
CONVERGE_WAIT=25   # 指纹收敛断言窗口（秒）——pollInterval 1s 的富余量
READY_WAIT=30      # Traefik 首次可握手等待（秒）

WORK=$(mktemp -d "${TMPDIR:-/tmp}/$P.XXXXXX") || exit 2
FORM_A_FP=''
FORM_B_FP=''
ROT_FP=''
ELAPSED=0

say() { printf '[%s] %s\n' "$P" "$*"; }
die() { printf '[%s] ERROR: %s\n' "$P" "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

cleanup_docker() { # 容器/网络/卷清理（复跑安全；不含 WORK——拓扑搭建前的
    # 预清理也调用它，当时本轮 WORK 内证书材料还在用）
    docker rm -f "$CTR_TRAEFIK" "$CTR_CONFIG" "$CTR_BACKEND" >/dev/null 2>&1
    docker network rm "$NET" >/dev/null 2>&1
    docker volume rm "$VOL" >/dev/null 2>&1
}
cleanup() {
    cleanup_docker
    rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# --------------------------------------------------------------- 前置检查
have docker || die 'docker not found on host'
have openssl || die 'openssl not found on host'
have curl || die 'curl not found on host'
docker info >/dev/null 2>&1 || die 'docker daemon unreachable'

# drun 是 docker run 的包一层（Git Bash/MSYS 兼容）：卷挂载参数
# "-v <name>:/<container-path>" 的尾段是 POSIX 绝对路径形态，部分 MSYS
# 运行时会把它误转成宿主 Windows 路径——调用点临时关闭参数转换。仅作用
# 于 docker（openssl/curl 的宿主路径参数依赖默认转换，不能全局关闭）。
drun() {
    MSYS2_ARG_CONV_EXCL='*' docker run "$@"
}

say 'pulling pinned images'
docker pull -q "$TRAEFIK_IMAGE" >/dev/null || die "image pull failed: $TRAEFIK_IMAGE"
docker pull -q "$BUSYBOX_IMAGE" >/dev/null || die "image pull failed: $BUSYBOX_IMAGE"

# --------------------------------------------------------------- 证书材料
# 两套独立自签对（不同私钥 → 指纹必不同）；SAN 含 spike.test 与路由
# Host 规则一致。证书生成走**显式 -config**（最小 req 配置 + SAN 段）：
# 宿主环境可能带全局 OPENSSL_CONF（本机实测 scoop openssl.cnf 与 Git Bash
# openssl 不兼容，req 的扩展段解析直接报错）——显式 config 把证书生成从
# 环境配置中解耦，任何 openssl >= 1.0.2 均可跑。
say "generating two self-signed pairs (SAN DNS:$TLS_HOST)"
cat > "$WORK/req.cnf" <<EOF
[req]
distinguished_name = dn
x509_extensions = v3_ext
prompt = no
[dn]
CN = $TLS_HOST
[v3_ext]
subjectAltName = DNS:$TLS_HOST
EOF
openssl req -x509 -newkey rsa:2048 -sha256 -days 2 -nodes \
    -config "$WORK/req.cnf" \
    -keyout "$WORK/key1.pem" -out "$WORK/cert1.pem" >/dev/null 2>&1 ||
    die 'self-signed pair #1 generation failed'
openssl req -x509 -newkey rsa:2048 -sha256 -days 2 -nodes \
    -config "$WORK/req.cnf" \
    -keyout "$WORK/key2.pem" -out "$WORK/cert2.pem" >/dev/null 2>&1 ||
    die 'self-signed pair #2 generation failed'

cert_fp() { openssl x509 -in "$1" -noout -fingerprint -sha256 2>/dev/null | cut -d= -f2; }
CERT1_FP=$(cert_fp "$WORK/cert1.pem")
CERT2_FP=$(cert_fp "$WORK/cert2.pem")
[ -n "$CERT1_FP" ] || die 'cert1 fingerprint computation failed'
[ -n "$CERT2_FP" ] || die 'cert2 fingerprint computation failed'
[ "$CERT1_FP" != "$CERT2_FP" ] || die 'cert1/cert2 fingerprints identical (generation bug)'
say "cert1 fp: $CERT1_FP"
say "cert2 fp: $CERT2_FP"

# ------------------------------------------------------------ 探针与配置合成
served_fp() { # 443 所服证书的 SHA-256 指纹（SNI = spike.test）
    openssl s_client -connect 127.0.0.1:"$HOST_PORT" -servername "$TLS_HOST" </dev/null 2>/dev/null |
        openssl x509 -noout -fingerprint -sha256 2>/dev/null | cut -d= -f2
}

route_status() { # 路由 HTTP 状态码（curl --resolve + -k 走 443；--noproxy
    # 显式绕过环境代理——HTTP_PROXY(S) 在场时 127.0.0.1 会被代理吞掉）
    curl -sk --noproxy '*' --max-time 5 -o /dev/null -w '%{http_code}' \
        --resolve "$TLS_HOST:$HOST_PORT:127.0.0.1" "https://$TLS_HOST:$HOST_PORT/" 2>/dev/null
}

route_body() { # 路由响应体（后端 200 时为 spike-ok）
    curl -sk --noproxy '*' --max-time 5 \
        --resolve "$TLS_HOST:$HOST_PORT:127.0.0.1" "https://$TLS_HOST:$HOST_PORT/" 2>/dev/null
}

pem_json() { # <file> — PEM → JSON 字符串体（换行转 \n 字面量；PEM 字符集
    # 内无引号/反斜杠，无需更多转义）
    sed -e ':a' -e 'N' -e '$!ba' -e 's/\n/\\n/g' "$1" | tr -d '\n'
}

emit_dynamic_config() { # <cert-field> <key-field> <cert.pem> <key.pem> <out.json>
    _cf=$1
    _kf=$2
    _cert=$(pem_json "$3")
    _key=$(pem_json "$4")
    {
        printf '{"http":{"routers":{"spike-router":{"rule":"Host(`%s`)","entryPoints":["websecure"],"service":"spike-svc","tls":{}}},' "$TLS_HOST"
        printf '"services":{"spike-svc":{"loadBalancer":{"servers":[{"url":"http://%s/"}]}}}},' "$CTR_BACKEND"
        printf '"tls":{"certificates":[{"%s":"%s","%s":"%s"}]}}\n' "$_cf" "$_cert" "$_kf" "$_key"
    } > "$5"
}

push_config() { # <json-file> — 写进配置卷（临时名 + mv 原子换入；httpd
    # 按请求读盘，Traefik 轮询即见新内容）
    drun --rm -i -v "$VOL:/cfg" "$BUSYBOX_IMAGE" sh -c \
        'cat > /cfg/.dynamic.json.tmp && chmod 0644 /cfg/.dynamic.json.tmp && mv -f /cfg/.dynamic.json.tmp /cfg/dynamic.json' \
        < "$1" || die 'failed to write dynamic config into volume'
}

wait_fp() { # <expected-fp> <max-seconds> — 命中置 ELAPSED（秒）
    _want=$1
    _max=$2
    _start=$(date +%s)
    while :; do
        if [ "$(served_fp)" = "$_want" ]; then
            ELAPSED=$(( $(date +%s) - _start ))
            return 0
        fi
        [ $(( $(date +%s) - _start )) -ge "$_max" ] && return 1
        sleep 1
    done
}

# --------------------------------------------------------------- 拓扑搭建
say 'cleanup stale resources (rerun safety)'
cleanup_docker

say 'creating topology'
docker network create "$NET" >/dev/null || die "network create failed: $NET"
docker volume create "$VOL" >/dev/null || die "volume create failed: $VOL"

drun -d --name "$CTR_BACKEND" --network "$NET" "$BUSYBOX_IMAGE" \
    sh -c 'mkdir -p /www && printf spike-ok > /www/index.html && exec httpd -f -p 80 -h /www' \
    >/dev/null || die 'backend container failed'
drun -d --name "$CTR_CONFIG" --network "$NET" -v "$VOL:/cfg" "$BUSYBOX_IMAGE" \
    httpd -f -p 80 -h /cfg \
    >/dev/null || die 'config endpoint container failed'
drun -d --name "$CTR_TRAEFIK" --network "$NET" -p "$HOST_PORT:443" "$TRAEFIK_IMAGE" \
    --entryPoints.websecure.address=:443 \
    --providers.http.endpoint="http://$CTR_CONFIG/dynamic.json" \
    --providers.http.pollInterval="$PROVIDER_POLL" \
    --providers.http.pollTimeout=5s \
    --log.level=DEBUG \
    >/dev/null || die "traefik container failed (host port $HOST_PORT busy? retry with SPIKE_V_MN_PORT=<free-port>)"

say 'waiting for traefik entrypoint (first TLS handshake)'
_ready=0
_i=0
while [ "$_i" -lt "$READY_WAIT" ]; do
    if [ -n "$(served_fp)" ]; then
        _ready=1
        break
    fi
    _i=$((_i + 1))
    sleep 1
done
if [ "$_ready" -ne 1 ]; then
    say '---- traefik log tail ----'
    docker logs --tail 30 "$CTR_TRAEFIK" 2>&1 || true
    die "traefik did not serve TLS within ${READY_WAIT}s"
fi
say "traefik up (default cert fp: $(served_fp))"

# ------------------------------------------------- form-A：设计原文键名
say '=== form-A: tls.certificates[certContent/keyContent] (design-doc literal keys) ==='
emit_dynamic_config certContent keyContent "$WORK/cert1.pem" "$WORK/key1.pem" "$WORK/form-a.json"
push_config "$WORK/form-a.json"
if wait_fp "$CERT1_FP" "$CONVERGE_WAIT"; then
    FORM_A_FP="$CERT1_FP"
    say "form-A: PASS — served fp == inline cert1 fp (converged in ${ELAPSED}s), route status $(route_status)"
else
    FORM_A_FP=$(served_fp)
    say "form-A: FAIL — served fp ($FORM_A_FP) != cert1 fp after ${CONVERGE_WAIT}s; route status $(route_status)"
    say 'form-A: certContent/keyContent are not part of the Traefik v3 JSON contract'
fi

# --------------------------- form-B：FileOrContent 契约（certFile/keyFile）
say '=== form-B: tls.certificates[certFile/keyFile] carrying inline PEM (Traefik FileOrContent contract) ==='
emit_dynamic_config certFile keyFile "$WORK/cert1.pem" "$WORK/key1.pem" "$WORK/form-b.json"
push_config "$WORK/form-b.json"
if wait_fp "$CERT1_FP" "$CONVERGE_WAIT"; then
    FORM_B_FP="$CERT1_FP"
    say "form-B: PASS — served fp == cert1 fp (converged in ${ELAPSED}s), route status $(route_status), body $(route_body)"
else
    FORM_B_FP=$(served_fp)
    say "form-B: FAIL — served fp ($FORM_B_FP) != cert1 fp after ${CONVERGE_WAIT}s"
fi

# ------------------------------------------------------------- 轮换断言
if [ "$FORM_B_FP" = "$CERT1_FP" ]; then
    say '=== rotate: swap inline cert content (cert1 -> cert2) ==='
    emit_dynamic_config certFile keyFile "$WORK/cert2.pem" "$WORK/key2.pem" "$WORK/form-b2.json"
    push_config "$WORK/form-b2.json"
    if wait_fp "$CERT2_FP" "$CONVERGE_WAIT"; then
        ROT_FP="$CERT2_FP"
        say "rotate: PASS — served fp == cert2 fp after ${ELAPSED}s (pollInterval=$PROVIDER_POLL), route status $(route_status)"
    else
        ROT_FP=$(served_fp)
        say "rotate: FAIL — served fp ($ROT_FP) != cert2 fp after ${CONVERGE_WAIT}s"
    fi
else
    say 'rotate: SKIPPED (form-B did not converge — no working inline-cert form to rotate)'
fi

# ---------------------------------------------------------------- 证据汇总
say '---- evidence ----'
say "cert1 fp        : $CERT1_FP"
say "cert2 fp        : $CERT2_FP"
say "formA served fp : $FORM_A_FP"
say "formB served fp : $FORM_B_FP"
say "rotate served fp: $ROT_FP"
say '---- traefik log excerpts (certificate/error lines) ----'
docker logs "$CTR_TRAEFIK" 2>&1 | grep -iE 'certificate|error|skip' | tail -8 || true

if [ "$FORM_A_FP" = "$CERT1_FP" ] && [ "$ROT_FP" = "$CERT2_FP" ]; then
    say 'VERDICT: PASS-LITERAL — certContent/keyContent works; design-doc §2.4 wording stands'
    exit 0
fi
if [ "$FORM_B_FP" = "$CERT1_FP" ] && [ "$ROT_FP" = "$CERT2_FP" ]; then
    say 'VERDICT: PASS-FILEORCONTENT — http provider + inline cert channel works via certFile/keyFile (FileOrContent); certContent/keyContent keys are NOT a Traefik contract (design-doc §2.4/E1-2 wording fix required)'
    exit 0
fi
say 'VERDICT: FAIL — no inline-certificate form converged; design-doc §2.4 certificate channel needs re-examination'
exit 1
