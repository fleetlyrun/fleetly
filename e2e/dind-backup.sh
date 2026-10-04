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
docker exec "$DIND_CID" docker exec "$MY_CID" mysql -ufleetly -p"$MY_PW" fleetly \
  -e "CREATE TABLE drill(k VARCHAR(32), v VARCHAR(32)); INSERT INTO drill VALUES (\"probe\", \"DRILL_OK\");" >/dev/null 2>&1 \
  || fail "mysql seed"
MY_COUNT=$(docker exec "$DIND_CID" docker exec "$MY_CID" mysql -N -ufleetly -p"$MY_PW" fleetly -e "SELECT count(*) FROM drill" 2>/dev/null)
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
MY_RESTORED=$(docker exec "$DIND_CID" docker exec "$MY_DST_CID" mysql -N -ufleetly -p"$MY_PW2" fleetly -e "SELECT v FROM drill LIMIT 1" 2>/dev/null)
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

log "ALL DRILLS GREEN: postgres/mysql/mongo stream restores + redis preseed restore + platform backup roundtrip"
