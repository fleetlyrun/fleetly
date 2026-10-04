#!/bin/sh
# e2e dind backup drill（F2.2，ADR-0039/ADR-0020 验收必过项）：四引擎恢复
# 演练——创建库 → 种子数据 → 触发备份 → verify（digest 重算）→ 恢复到
# 新库（pg/mysql/mongo 流式 + redis 预置卷）→ 数据断言；Platform Backup
# 本地仓 roundtrip（restic 真跑：节拍锚 + 快照在场 + 仓密排除）。
#
# 引号纪律：种子/断言一律 argv 级 docker exec（无内层 sh -c）；SQL/JS 用
# 美元引用或双引号字符串（pg 的 $$…$$、mysql/mongo 的 "…"）——全脚本零
# 单引号嵌套。密码经容器内材料文件宿侧捕获（演练专用面；生产面永不回
# 显）。前置：本机 docker 可用；dind 内出网可用（镜像拉取 + restic）。
set -eu

export MSYS_NO_PATHCONV=1

WORKDIR="$(mktemp -d)"
DIND_CID=""

cleanup() {
  if [ -n "$DIND_CID" ]; then
    docker rm -f "$DIND_CID" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT INT TERM

log() { printf '==> %s\n' "$1"; }
# fail 带 dind 侧诊断（service 快照 + fleetlyd 日志尾——postgres 卡 pending
# 类问题的取证面；升级 e2e 的失败出口同款文化）。
fail() {
  echo "DRILL FAILED: $1" >&2
  if [ -n "$DIND_CID" ]; then
    echo "--- services ---" >&2
    docker exec "$DIND_CID" docker service ls --format "{{.Name}} {{.Replicas}} {{.Image}}" >&2 || true
    echo "--- containers ---" >&2
    docker exec "$DIND_CID" docker ps -a --format "{{.Names}} {{.Status}}" | head -10 >&2 || true
    echo "--- last db container log ---" >&2
    docker exec "$DIND_CID" sh -c "docker ps -a --filter name=fleetly-db --format {{.ID}} | head -1 | xargs -r docker logs --tail 25" >&2 || true
    echo "--- utility container state + log ---" >&2
    docker exec "$DIND_CID" sh -c "docker ps -a --filter name=fleetly-utility --format {{.ID}} | head -1 | xargs -r docker logs --tail 25" >&2 || true
    docker exec "$DIND_CID" sh -c "docker ps -a --filter name=fleetly-utility --format {{.ID}} | head -1 | xargs -r docker top" >&2 || true
    echo "--- fleetlyd log tail ---" >&2
    docker exec "$DIND_CID" tail -40 /var/log/fleetlyd.log >&2 || true
  fi
  exit 1
}

log "cross-compiling fleetlyd + fleetly (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly

log "starting dind container"
DIND_CID=$(docker run -d --privileged --name fleetly-e2e-backup-"$$" \
  -e DOCKER_TLS_CERTDIR= \
  docker:29-dind)

wait_docker() {
  i=0
  while [ "$i" -lt 60 ]; do
    if docker exec "$DIND_CID" docker info >/dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  return 1
}
log "waiting for dind daemon"
wait_docker || fail "dind daemon did not become ready"

# restic（Platform Backup 钉版 0.19.1，与 PinnedResticVersion 同 commit 纪律）。
# 通道二选一：宿侧资产 <repo>/.tmp-local-restic 在场 → docker cp 注入
#（出站波动环境的确定性通道——资产经任意机器下载放置）；缺席 → dind 内
# busybox wget 直下（install.sh 的 curl 通道在 dind 镜像缺席；CI 常规路径）。
log "installing restic 0.19.1 into dind (platform backup engine)"
if [ -f ".tmp-local-restic" ]; then
  log "using host-side restic asset (.tmp-local-restic)"
  # 容器侧路径用 /root（/tmp 是 MSYS 挂载点，宿侧 Git-Bash 会转译 docker
  # cp 的容器路径——dind-smoke 实证坑）。
  docker cp .tmp-local-restic "$DIND_CID":/root/restic
  docker exec "$DIND_CID" chmod +x /root/restic
  docker exec "$DIND_CID" mv /root/restic /usr/local/bin/restic
else
  docker exec "$DIND_CID" sh -c \
    "wget -q -O /root/restic.bz2 https://github.com/restic/restic/releases/download/v0.19.1/restic_0.19.1_linux_amd64.bz2 && bunzip2 -f /root/restic.bz2 && chmod +x /root/restic && mv /root/restic /usr/local/bin/restic" \
    || fail "restic install failed (drill requires network or a .tmp-local-restic asset)"
fi
docker exec "$DIND_CID" restic version | grep -q 0.19.1 || fail "restic version mismatch"

# 镜像通道：宿侧已有 → image save | load（离线确定、smoke 同款）；缺席 →
# dind 内拉取（单次重试——出站波动的最小重试面）。
log "preloading engine template images into dind"
for img in postgres:17-bookworm mysql:8.4 mongo:8.0 redis:7.4; do
  if docker image inspect "$img" >/dev/null 2>&1; then
    docker image save "$img" | docker exec -i "$DIND_CID" docker load >/dev/null || fail "load $img"
  else
    docker exec "$DIND_CID" docker pull -q "$img" >/dev/null \
      || docker exec "$DIND_CID" docker pull -q "$img" >/dev/null \
      || fail "pull $img"
  fi
done

log "running install.sh inside dind (FLEETLY_BIN_DIR mode)"
docker cp "$WORKDIR/bins" "$DIND_CID":/root/bins
docker cp install.sh "$DIND_CID":/root/install.sh
docker exec -e FLEETLY_BIN_DIR=/root/bins "$DIND_CID" sh /root/install.sh

CURRENT_TOKEN=$(docker exec "$DIND_CID" sh -c "tr -d \"\\r\\n\" < /var/lib/fleetly/bootstrap-token")
[ -n "$CURRENT_TOKEN" ] || fail "bootstrap token missing"

cli() {
  docker exec -e FLEETLY_ADDR=127.0.0.1:9080 -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$DIND_CID" fleetly "$@"
}

NEW_TOKEN=$(docker exec -e FLEETLY_ADDR=127.0.0.1:9080 "$DIND_CID" \
  fleetly init --json --token "$CURRENT_TOKEN" alice \
  | sed -n "s/.*\"secret\": *\"\([^\"]*\)\".*/\1/p" | head -1)
[ -n "$NEW_TOKEN" ] || fail "fleetly init did not mint a CLI token"
CURRENT_TOKEN="$NEW_TOKEN"

log "creating project"
cli projects create shop >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n "s/.*\"id\": *\"\([^\"]*\)\".*/\1/p" | head -1)
[ -n "$PROJECT_ID" ] || fail "project id not extracted"

# db_id_by_name 经列表回显定位（name → id；--json 列表 name 后于 id，
# 逐块 grep）。
db_id_by_name() {
  cli --json databases list --project "$PROJECT_ID" \
    | grep -B 5 "\"name\": *\"$1\"" | sed -n "s/.*\"id\": *\"\([^\"]*\)\".*/\1/p" | head -1
}

db_container_id() {
  # 载体名小写（sanitizeNamePart 归一；请求侧 ULID 大写）。
  dbid=$(printf '%s' "$1" | tr 'A-Z' 'a-z')
  docker exec "$DIND_CID" docker ps -q --filter "name=fleetly-db-$dbid" | head -1
}

wait_db_running() {
  dbid="$1"; i=0; status=""
  while [ "$i" -lt 240 ]; do
    status=$(cli --json databases get "$dbid" | sed -n "s/.*\"status\": *\"\([^\"]*\)\".*/\1/p" | head -1)
    [ "$status" = "running" ] && return 0
    case "$status" in degraded|stopped) fail "database $dbid reached $status";; esac
    i=$((i + 1)); sleep 2
  done
  fail "database $dbid did not reach running (last=$status)"
}

wait_backup_succeeded() {
  dbid="$1"; i=0; st=""
  while [ "$i" -lt 120 ]; do
    st=$(cli --json databases backups "$dbid" | sed -n "s/.*\"status\": *\"\([^\"]*\)\".*/\1/p" | head -1)
    [ "$st" = "succeeded" ] && return 0
    [ "$st" = "failed" ] && fail "backup of $dbid failed"
    i=$((i + 1)); sleep 2
  done
  fail "backup of $dbid did not succeed (last=$st)"
}

wait_restore_done() {
  dbid="$1"; i=0; status=""
  while [ "$i" -lt 240 ]; do
    if ! cli --json databases get "$dbid" | grep -q "restore_from_backup"; then
      status=$(cli --json databases get "$dbid" | sed -n "s/.*\"status\": *\"\([^\"]*\)\".*/\1/p" | head -1)
      [ "$status" = "running" ] && return 0
    fi
    i=$((i + 1)); sleep 2
  done
  fail "restore into $dbid did not complete (status=$status)"
}

latest_backup_id() {
  cli --json databases backups "$1" | sed -n "s/.*\"id\": *\"\([^\"]*\)\".*/\1/p" | head -1
}

# backup_and_verify：触发 → 等成功 → digest 验证，回显备份 id。
backup_and_verify() {
  dbid="$1"
  cli databases backup "$dbid" >/dev/null
  wait_backup_succeeded "$dbid"
  bid=$(latest_backup_id "$dbid")
  cli databases verify "$bid" --json | grep -q "\"ok\": *true" || fail "verify of $bid reported not-ok"
  echo "$bid"
}

# restore_target：建恢复目标库并回显 id（旗标前置——Go flag 首位置参停析）。
restore_target() {
  eng="$1"; name="$2"; bid="$3"
  cli databases create --project "$PROJECT_ID" --engine "$eng" \
    --restore-from-backup "$bid" "$name" >/dev/null
  db_id_by_name "$name"
}

# ---- postgres（流式；容器内本地 socket 免密；$$ 美元引用零单引号）----
log "[postgres] creating source database"
cli databases create --project "$PROJECT_ID" --engine postgres pgshop >/dev/null
PG_SRC=$(db_id_by_name pgshop)
[ -n "$PG_SRC" ] || fail "postgres source id"
wait_db_running "$PG_SRC"
PG_CID=$(db_container_id "$PG_SRC")
[ -n "$PG_CID" ] || fail "postgres source container"

log "[postgres] seeding probe data"
docker exec "$DIND_CID" docker exec "$PG_CID" psql -U fleetly -d fleetly \
  -c "CREATE TABLE drill(k text, v text); INSERT INTO drill VALUES (\$\$probe\$\$, \$\$DRILL_OK\$\$);" >/dev/null \
  || fail "postgres seed"
PG_COUNT=$(docker exec "$DIND_CID" docker exec "$PG_CID" psql -U fleetly -d fleetly -tAc "SELECT count(*) FROM drill")
[ "$PG_COUNT" = "1" ] || fail "postgres seed count=$PG_COUNT"

# 任务替换存活锚（2026-10-04 staging 实录回归钉）：postgres 系父目录挂法
# 曾被镜像 VOLUME 声明的匿名卷遮蔽命名卷 data/ 子目录——任务替换 = 新空
# 卷 = initdb 空库。dbtemplate 以显式 PGDATA 收口；本锚 --force 滚一次任
# 务，种子行必须原样在场（挂载面任何回归在此即红）。
log "[postgres] task-replacement survival anchor"
PG_OLD_CID="$PG_CID"
docker exec "$DIND_CID" docker service update --force \
  "fleetly-db-$(printf '%s' "$PG_SRC" | tr 'A-Z' 'a-z')" >/dev/null \
  || fail "postgres force replacement"
i=0
while [ "$i" -lt 120 ]; do
  PG_CID=$(db_container_id "$PG_SRC")
  if [ -n "$PG_CID" ] && [ "$PG_CID" != "$PG_OLD_CID" ]; then
    if docker exec "$DIND_CID" docker exec "$PG_CID" psql -U fleetly -d fleetly -tAc "SELECT 1" >/dev/null 2>&1; then
      break
    fi
  fi
  i=$((i + 1)); sleep 2
done
[ -n "$PG_CID" ] && [ "$PG_CID" != "$PG_OLD_CID" ] || fail "postgres replacement container never served"
PG_COUNT2=$(docker exec "$DIND_CID" docker exec "$PG_CID" psql -U fleetly -d fleetly -tAc "SELECT count(*) FROM drill")
[ "$PG_COUNT2" = "1" ] || fail "postgres data lost across task replacement (count=$PG_COUNT2)"
log "[postgres] data survived task replacement"

log "[postgres] backup + verify"
PG_BACKUP=$(backup_and_verify "$PG_SRC")

log "[postgres] restoring into a new database"
PG_DST=$(restore_target postgres pgshop-restored "$PG_BACKUP")
[ -n "$PG_DST" ] || fail "postgres restore target id"
wait_restore_done "$PG_DST"
PG_DST_CID=$(db_container_id "$PG_DST")
[ -n "$PG_DST_CID" ] || fail "postgres restore target container"
PG_RESTORED=$(docker exec "$DIND_CID" docker exec "$PG_DST_CID" psql -U fleetly -d fleetly -tAc "SELECT v FROM drill LIMIT 1")
[ "$PG_RESTORED" = "DRILL_OK" ] || fail "postgres restored value=$PG_RESTORED"
log "[postgres] roundtrip green"

# ---- mysql（流式；密码宿侧捕获自容器内密码文件）----
log "[mysql] creating source database"
cli databases create --project "$PROJECT_ID" --engine mysql myshop >/dev/null
MY_SRC=$(db_id_by_name myshop)
[ -n "$MY_SRC" ] || fail "mysql source id"
wait_db_running "$MY_SRC"
MY_CID=$(db_container_id "$MY_SRC")
[ -n "$MY_CID" ] || fail "mysql source container"
MY_PW=$(docker exec "$DIND_CID" docker exec "$MY_CID" cat /run/secrets/database-password)

log "[mysql] seeding probe data"
# -h 127.0.0.1 显式 TCP + 单引号 SQL 字面量（双引号字符串依赖 sql_mode
# 的 ANSI_QUOTES 位——不赌默认位）；种子命令不吞 stderr（CI 失败面可见）。
docker exec "$DIND_CID" docker exec "$MY_CID" mysql -h 127.0.0.1 -ufleetly -p"$MY_PW" fleetly \
  -e "CREATE TABLE drill(k VARCHAR(32), v VARCHAR(32)); INSERT INTO drill VALUES ('probe', 'DRILL_OK');" \
  || fail "mysql seed"
MY_COUNT=$(docker exec "$DIND_CID" docker exec "$MY_CID" mysql -N -h 127.0.0.1 -ufleetly -p"$MY_PW" fleetly -e "SELECT count(*) FROM drill" 2>/dev/null)
[ "$MY_COUNT" = "1" ] || fail "mysql seed count=$MY_COUNT"

log "[mysql] backup + verify"
MY_BACKUP=$(backup_and_verify "$MY_SRC")

log "[mysql] restoring into a new database"
MY_DST=$(restore_target mysql myshop-restored "$MY_BACKUP")
[ -n "$MY_DST" ] || fail "mysql restore target id"
wait_restore_done "$MY_DST"
MY_DST_CID=$(db_container_id "$MY_DST")
[ -n "$MY_DST_CID" ] || fail "mysql restore target container"
MY_PW2=$(docker exec "$DIND_CID" docker exec "$MY_DST_CID" cat /run/secrets/database-password)
MY_RESTORED=$(docker exec "$DIND_CID" docker exec "$MY_DST_CID" mysql -N -h 127.0.0.1 -ufleetly -p"$MY_PW2" fleetly -e "SELECT v FROM drill LIMIT 1" 2>/dev/null)
[ "$MY_RESTORED" = "DRILL_OK" ] || fail "mysql restored value=$MY_RESTORED"
log "[mysql] roundtrip green"

# ---- mongo（流式；密码从 init 脚本材料 grep hex 铸式）----
log "[mongo] creating source database"
cli databases create --project "$PROJECT_ID" --engine mongo moshop >/dev/null
MO_SRC=$(db_id_by_name moshop)
[ -n "$MO_SRC" ] || fail "mongo source id"
wait_db_running "$MO_SRC"
MO_CID=$(db_container_id "$MO_SRC")
[ -n "$MO_CID" ] || fail "mongo source container"
MO_PW=$(docker exec "$DIND_CID" docker exec "$MO_CID" grep -oE "pwd: \"[0-9a-f]{48}\"|pwd: '[0-9a-f]{48}'" /run/secrets/database-mongo-init | grep -oE "[0-9a-f]{48}")
[ -n "$MO_PW" ] || fail "mongo password extraction"

log "[mongo] seeding probe data"
docker exec "$DIND_CID" docker exec "$MO_CID" mongosh "mongodb://fleetly:$MO_PW@127.0.0.1:27017/fleetly" \
  --quiet --eval "db.drill.insertOne({k: \"probe\", v: \"DRILL_OK\"})" >/dev/null \
  || fail "mongo seed"
MO_COUNT=$(docker exec "$DIND_CID" docker exec "$MO_CID" mongosh "mongodb://fleetly:$MO_PW@127.0.0.1:27017/fleetly" \
  --quiet --eval "db.drill.countDocuments({})")
[ "$MO_COUNT" = "1" ] || fail "mongo seed count=$MO_COUNT"

log "[mongo] backup + verify"
MO_BACKUP=$(backup_and_verify "$MO_SRC")

log "[mongo] restoring into a new database"
MO_DST=$(restore_target mongo moshop-restored "$MO_BACKUP")
[ -n "$MO_DST" ] || fail "mongo restore target id"
wait_restore_done "$MO_DST"
MO_DST_CID=$(db_container_id "$MO_DST")
[ -n "$MO_DST_CID" ] || fail "mongo restore target container"
MO_PW2=$(docker exec "$DIND_CID" docker exec "$MO_DST_CID" grep -oE "pwd: \"[0-9a-f]{48}\"|pwd: '[0-9a-f]{48}'" /run/secrets/database-mongo-init | grep -oE "[0-9a-f]{48}")
MO_RESTORED=$(docker exec "$DIND_CID" docker exec "$MO_DST_CID" mongosh "mongodb://fleetly:$MO_PW2@127.0.0.1:27017/fleetly" \
  --quiet --eval "db.drill.findOne().v")
[ "$MO_RESTORED" = "DRILL_OK" ] || fail "mongo restored value=$MO_RESTORED"
log "[mongo] roundtrip green"

# ---- redis（预置卷；RDB 仅启动装载——恢复 = 新库首启装预置卷）----
log "[redis] creating source database"
cli databases create --project "$PROJECT_ID" --engine redis rdshop >/dev/null
RD_SRC=$(db_id_by_name rdshop)
[ -n "$RD_SRC" ] || fail "redis source id"
wait_db_running "$RD_SRC"
RD_CID=$(db_container_id "$RD_SRC")
[ -n "$RD_CID" ] || fail "redis source container"
RD_PW=$(docker exec "$DIND_CID" docker exec "$RD_CID" grep -oE "requirepass [0-9a-f]+" /run/secrets/database-redis-conf | cut -d" " -f2)
[ -n "$RD_PW" ] || fail "redis password extraction"

log "[redis] seeding probe data"
docker exec "$DIND_CID" docker exec "$RD_CID" redis-cli -a "$RD_PW" --no-auth-warning SET drill:probe DRILL_OK >/dev/null \
  || fail "redis seed"

log "[redis] backup + verify"
RD_BACKUP=$(backup_and_verify "$RD_SRC")

log "[redis] restoring into a new database (volume pre-seed path)"
RD_DST=$(restore_target redis rdshop-restored "$RD_BACKUP")
[ -n "$RD_DST" ] || fail "redis restore target id"
wait_restore_done "$RD_DST"
RD_DST_CID=$(db_container_id "$RD_DST")
[ -n "$RD_DST_CID" ] || fail "redis restore target container"
RD_PW2=$(docker exec "$DIND_CID" docker exec "$RD_DST_CID" grep -oE "requirepass [0-9a-f]+" /run/secrets/database-redis-conf | cut -d" " -f2)
RD_RESTORED=$(docker exec "$DIND_CID" docker exec "$RD_DST_CID" redis-cli -a "$RD_PW2" --no-auth-warning GET drill:probe)
[ "$RD_RESTORED" = "DRILL_OK" ] || fail "redis restored value=$RD_RESTORED"
log "[redis] roundtrip green (preseed volume loaded at first start)"

# ---- Platform Backup 本地仓 roundtrip（restic 真跑）----
log "[platform] waiting for the first platform backup (restic local repo)"
i=0
while [ "$i" -lt 60 ]; do
  if docker exec "$DIND_CID" sh -c "test -f /var/lib/fleetly/platform-backups/last-run"; then
    break
  fi
  i=$((i + 1)); sleep 2
done
docker exec "$DIND_CID" sh -c "test -f /var/lib/fleetly/platform-backups/last-run" \
  || fail "platform backup anchor never appeared (fleetlyd log: $(docker exec "$DIND_CID" tail -5 /var/log/fleetlyd.log 2>/dev/null))"

log "[platform] asserting snapshots exist and the repo key is excluded"
PLATFORM_CHECK=$(docker exec "$DIND_CID" sh -c \
  "RESTIC_PASSWORD=\$(cat /var/lib/fleetly/keys/platform-backup.key) restic -r /var/lib/fleetly/platform-backups/restic snapshots --json" \
  | grep -oE "\"short_id\"" | head -1)
[ -n "$PLATFORM_CHECK" ] || fail "no restic snapshots in the local repo"
# 文件节点级断言：restic ls --json 的 snapshot 头行携带 excludes 元数据
#（排除清单本身含密钥路径——按 name 字段精确断言防误报）。
KEY_IN_SNAPSHOT=$(docker exec "$DIND_CID" sh -c \
  "RESTIC_PASSWORD=\$(cat /var/lib/fleetly/keys/platform-backup.key) restic -r /var/lib/fleetly/platform-backups/restic ls latest --json" \
  | grep -c "\"name\":\"platform-backup.key\"" || true)
[ "$KEY_IN_SNAPSHOT" = "0" ] || fail "repo password key leaked into its own snapshot (self-lock circularity)"

log "[platform] local repo roundtrip green"

# ---- S3 ObjectStore 腿（F2.8，ADR-0042）：silo 假端点 + 在场驱动换装 ----
# MinIO 社区版 2026-02 EOL，S3 兼容假端点用社区续命版 silo（pgsty/silo 钉版；
# 镜像自带 mcli——建桶与断言零额外镜像）。五元组经 /etc/fleetlyd.env 注入
# （env 键 = FLEETLY_PLATFORM_BACKUP_S3_*，lynx 结构体通道逐叶生效）+ 同文件
# 重启 fleetlyd：装配选择切 s3 Provider，restic 外置仓同批指向同桶
# （prefix=platform-repo——对象键 backups/ 与仓前缀双命名空间，ADR-0042 决策 1）。
SILO_IMAGE=pgsty/silo:RELEASE.2026-09-16T00-00-00Z
log "[s3] starting silo as the S3-compatible fake endpoint ($SILO_IMAGE)"
if docker image inspect "$SILO_IMAGE" >/dev/null 2>&1; then
  docker image save "$SILO_IMAGE" | docker exec -i "$DIND_CID" docker load >/dev/null || fail "load $SILO_IMAGE"
else
  docker exec "$DIND_CID" docker pull -q "$SILO_IMAGE" >/dev/null \
    || docker exec "$DIND_CID" docker pull -q "$SILO_IMAGE" >/dev/null \
    || fail "pull $SILO_IMAGE"
fi
docker exec "$DIND_CID" docker run -d --name fleetly-e2e-silo \
  -p 127.0.0.1:9000:9000 \
  -e MINIO_ROOT_USER=fleetly-e2e \
  -e MINIO_ROOT_PASSWORD=e2e-offsite-key \
  "$SILO_IMAGE" server /data >/dev/null || fail "silo start"
# 就绪门：mcli 的 alias set 自身会签名探测端点——服务器未就绪时 connection
# refused（CI 两轮实录：run 后 ~2.5s 仍在初始化；nc -z 探到的是 dockerd 的
# userland-proxy 端口绑定，不是 silo 本体，不可用作门）。对 alias set 重试
# 即就绪探针，零健康路径/镜像工具面假设。
i=0
while [ "$i" -lt 30 ]; do
  if docker exec "$DIND_CID" docker exec fleetly-e2e-silo \
    mcli alias set e2e http://127.0.0.1:9000 fleetly-e2e e2e-offsite-key >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1)); sleep 1
done
[ "$i" -lt 30 ] || fail "silo never became ready (mcli alias probe)"
i=0
while [ "$i" -lt 30 ]; do
  docker exec "$DIND_CID" docker exec fleetly-e2e-silo mcli mb e2e/fleetly-backups >/dev/null 2>&1 && break
  docker exec "$DIND_CID" docker exec fleetly-e2e-silo mcli ls e2e/fleetly-backups >/dev/null 2>&1 && break
  i=$((i + 1)); sleep 1
done
[ "$i" -lt 30 ] || fail "silo never became ready"

log "[s3] switching fleetlyd to the s3 object store (platform_backup.s3 five-tuple)"
LOCAL_OBJECTS_BEFORE=$(docker exec "$DIND_CID" sh -c "find /var/lib/fleetly/backups -type f | wc -l")
docker exec "$DIND_CID" sh -c "printf '%s\n' \
  FLEETLY_PLATFORM_BACKUP_S3_ENDPOINT=http://127.0.0.1:9000 \
  FLEETLY_PLATFORM_BACKUP_S3_BUCKET=fleetly-backups \
  FLEETLY_PLATFORM_BACKUP_S3_PREFIX=platform-repo \
  FLEETLY_PLATFORM_BACKUP_S3_ACCESS_KEY_ID=fleetly-e2e \
  FLEETLY_PLATFORM_BACKUP_S3_SECRET_ACCESS_KEY=e2e-offsite-key \
  >> /etc/fleetlyd.env" || fail "append s3 env"
docker exec "$DIND_CID" pkill -TERM fleetlyd || true
i=0
while [ "$i" -lt 30 ] && docker exec "$DIND_CID" pgrep fleetlyd >/dev/null 2>&1; do
  i=$((i + 1)); sleep 1
done
docker exec "$DIND_CID" pgrep fleetlyd >/dev/null 2>&1 && fail "fleetlyd did not stop"
# 同文件同源重启（dind-upgrade.sh 的既证形态：ENVARGS 展开 + 空回退）。
docker exec "$DIND_CID" sh -c \
  'ENVARGS="$(grep -v "^$" /etc/fleetlyd.env 2>/dev/null | tr "\n" " ")"; [ -n "$ENVARGS" ] || ENVARGS="FLEETLY_DATA_ROOT=/var/lib/fleetly"; setsid env $ENVARGS /usr/local/bin/fleetlyd >>/var/log/fleetlyd.log 2>&1 </dev/null &' || fail "fleetlyd restart"
i=0
while [ "$i" -lt 60 ]; do
  cli status >/dev/null 2>&1 && break
  i=$((i + 1)); sleep 1
done
[ "$i" -lt 60 ] || fail "fleetlyd did not come back with the s3 config"
# 装配选择锚：daemon 日志的 capability faces 行须含 objectstore + s3。zap
# 字段序不稳定（kind 与 provider 不保证相邻——run 37217928650 实录），三段
# 式行内过滤与字段序无关。
docker exec "$DIND_CID" sh -c \
  'grep "capability faces" /var/log/fleetlyd.log | grep "\"kind\":\"objectstore\"" | grep "\"provider\":\"s3\"" | tail -1' \
  | grep -q . \
  || fail "objectstore provider did not switch to s3 (log tail: $(docker exec "$DIND_CID" tail -5 /var/log/fleetlyd.log 2>/dev/null))"

log "[s3] database backup lands in the bucket (fresh postgres drill)"
cli databases create --project "$PROJECT_ID" --engine postgres pgs3 >/dev/null
PGS3_SRC=$(db_id_by_name pgs3)
[ -n "$PGS3_SRC" ] || fail "pgs3 source id"
wait_db_running "$PGS3_SRC"
PGS3_CID=$(db_container_id "$PGS3_SRC")
[ -n "$PGS3_CID" ] || fail "pgs3 source container"
docker exec "$DIND_CID" docker exec "$PGS3_CID" psql -U fleetly -d fleetly \
  -c "CREATE TABLE drill(k text, v text); INSERT INTO drill VALUES (\$\$probe\$\$, \$\$DRILL_S3\$\$);" >/dev/null \
  || fail "pgs3 seed"
PGS3_BACKUP=$(backup_and_verify "$PGS3_SRC")
S3_KEY=$(cli --json databases backups "$PGS3_SRC" \
  | sed -n "s/.*\"object_key\": *\"\([^\"]*\)\".*/\1/p" | head -1)
[ -n "$S3_KEY" ] || fail "backup row carries no object_key"
case "$S3_KEY" in
  backups/"$PROJECT_ID"/"$PGS3_SRC"/*) ;;
  *) fail "object key $S3_KEY does not follow backups/<project>/<database>/<ts> (project=$PROJECT_ID db=$PGS3_SRC)" ;;
esac
log "[s3] object present in the bucket with ledger size (key: $S3_KEY)"
S3_SIZE=$(cli --json databases backups "$PGS3_SRC" \
  | sed -n "s/.*\"size_bytes\": *\"\([0-9]*\)\".*/\1/p" | head -1)
[ -n "$S3_SIZE" ] || fail "backup row carries no size_bytes"
docker exec "$DIND_CID" docker exec fleetly-e2e-silo \
  mcli stat --json e2e/fleetly-backups/"$S3_KEY" | grep -q "\"size\": *$S3_SIZE" \
  || fail "object $S3_KEY missing in bucket or size mismatch (ledger=$S3_SIZE)"
LOCAL_OBJECTS_AFTER=$(docker exec "$DIND_CID" sh -c "find /var/lib/fleetly/backups -type f | wc -l")
[ "$LOCAL_OBJECTS_AFTER" = "$LOCAL_OBJECTS_BEFORE" ] \
  || fail "local object count changed during the s3 leg ($LOCAL_OBJECTS_BEFORE -> $LOCAL_OBJECTS_AFTER): backup did not go off-machine"

log "[s3] restoring from the off-machine backup"
PGS3_DST=$(restore_target postgres pgs3-restored "$PGS3_BACKUP")
[ -n "$PGS3_DST" ] || fail "pgs3 restore target id"
wait_restore_done "$PGS3_DST"
PGS3_DST_CID=$(db_container_id "$PGS3_DST")
[ -n "$PGS3_DST_CID" ] || fail "pgs3 restore target container"
PGS3_RESTORED=$(docker exec "$DIND_CID" docker exec "$PGS3_DST_CID" psql -U fleetly -d fleetly -tAc "SELECT v FROM drill LIMIT 1")
[ "$PGS3_RESTORED" = "DRILL_S3" ] || fail "pgs3 restored value=$PGS3_RESTORED"

log "[s3] platform backup round-trips into the same bucket (restic external repo)"
cli platform backup --json | sed -n "s/.*\"id\": *\"\([^\"]*\)\".*/\1/p" | head -1 | grep -q . \
  || fail "platform backup against the s3 repo failed"
docker exec "$DIND_CID" docker exec fleetly-e2e-silo mcli ls e2e/fleetly-backups/platform-repo/ \
  | grep -q "data" || fail "restic external repo not present under the platform-repo prefix"

log "[s3] offsite alert stays resolved with s3 configured"
cli --json alerts list | grep -A 4 '"rule_id": *"platform-offsite-backup"' | grep -q '"state": *"ok"' \
  || fail "platform-offsite-backup is not ok with the s3 repo configured"

log "[s3] off-machine leg green (object store + restic repo dual-track)"

log "ALL DRILLS GREEN: postgres/mysql/mongo stream restores + redis preseed restore + platform backup roundtrip + s3 off-machine leg"
