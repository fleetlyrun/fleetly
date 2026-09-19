#!/bin/sh
# e2e/nightly — host-side regression runner.
#
# Boots privileged docker:29.8.1-dind containers (one per suite; three for
# the dual-dind v6 swarm), stages scripts/binaries via exec+stdin (docker cp
# host->dind silently drops files on Engine 29.x, e2e/README.md known
# issue), runs the per-suite assertion script inside, and cleans up with a
# dind-log dump on failure. Designed to run unchanged in GitHub Actions
# (ubuntu-latest, bash) and locally (Git Bash on Windows; WSL also fine).
#
# usage: run.sh <suite>...     suite = v1 | v2 | v3 | v4 | v6 | all
# env:
#   DIND_IMAGE       dind image (default docker:29.8.1-dind)
#   DIND_EXTRA_ARGS  extra dockerd args appended AFTER the image ref (they go
#                    to dockerd via the dind entrypoint, not to `docker run`),
#                    e.g. "--storage-driver overlay2" for the storage leg
#
# Suite -> engine topology:
#   v1  one dind (infra-b + v1.sh)             spike/b B1/B2
#   v2  one dedicated fresh dind (v2.sh + host-side dockerd-log grep)
#                                              spike/a E5
#   v3  one dind (infra-b + v3.sh)             spike/b V3
#   v4  one dind (infra-b + v4.sh)             spike/b V4 (b3d + b3 subset)
#   v6  three dinds on a host bridge (mgr/w1/w2, swarm join, v6.sh)
#                                              spike/c C3a/C3b/C4a
# The probe binaries are cross-compiled from the spike modules (referenced,
# not copied): spike/b/cmd/probe and spike/c/cmd/probe (GOWORK=off, static
# linux/amd64).
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
NLROOT="$ROOT/e2e/nightly"
# generic helpers (nl/pass/fail/assert/grep_count) shared with the in-dind
# scripts; the docker-based wait_* helpers in lib.sh are inner-only and are
# never called from the host side.
. "$NLROOT/lib.sh"
DIND_IMAGE="${DIND_IMAGE:-docker:29.8.1-dind}"
DIND_EXTRA_ARGS="${DIND_EXTRA_ARGS:-}"
BR_NET=fleetly-nightly-br
BR_SUBNET=10.213.0.0/24
TMP=$(mktemp -d)
mkdir -p "$TMP/artifacts"

log() { printf '[nightly %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() { log "FATAL: $*"; exit 1; }
usage() {
    echo "usage: $0 <suite>...   suite = v1 | v2 | v3 | v4 | v6 | all" >&2
    exit 2
}

SUITE_DINDS=""
ACTIVE_NET=""
FAILED_SUITES=""
PASSED_SUITES=""

dump_and_forget() { # <dind...>  dump log tail then remove
    for d in $SUITE_DINDS; do
        log "---- dind log tail: $d ----"
        docker logs "$d" --tail 120 2>&1 | tail -60 || true
    done
}
suite_cleanup() {
    for d in $SUITE_DINDS; do
        docker rm -f "$d" >/dev/null 2>&1 || true
    done
    if [ -n "$ACTIVE_NET" ]; then
        docker network rm "$ACTIVE_NET" >/dev/null 2>&1 || true
        ACTIVE_NET=""
    fi
    SUITE_DINDS=""
}
on_exit() {
    rc=$?
    if [ -n "$SUITE_DINDS" ]; then
        [ "$rc" -ne 0 ] && dump_and_forget
        suite_cleanup
    fi
    log "cleanup done (exit=$rc)"
    exit "$rc"
}
trap on_exit EXIT INT TERM

# defensive sweep: leftovers of a previously crashed run would poison
# assertions (ghost processes, spike/a README #11)
sweep() {
    leftovers=$(docker ps -aq --filter name=fleetly-nightly- 2>/dev/null || true)
    if [ -n "$leftovers" ]; then
        log "WARN removing leftover nightly containers: $leftovers"
        echo "$leftovers" | xargs docker rm -f >/dev/null 2>&1 || true
    fi
    docker network rm "$BR_NET" >/dev/null 2>&1 || true
}

dind_up() { # <name> [extra docker run args...]
    n=$1
    shift
    docker rm -f "$n" >/dev/null 2>&1 || true
    # shellcheck disable=SC2086
    docker run -d --name "$n" --privileged "$@" "$DIND_IMAGE" $DIND_EXTRA_ARGS >/dev/null ||
        die "docker run $n"
    SUITE_DINDS="$SUITE_DINDS $n"
    i=0
    while ! docker exec "$n" docker info >/dev/null 2>&1; do
        i=$((i + 2))
        if [ "$i" -ge 60 ]; then
            docker logs "$n" --tail 40 || true
            die "inner dockerd of $n not ready within 60s"
        fi
        sleep 2
    done
    log "dind $n ready (engine $(docker exec "$n" docker version --format '{{.Server.Version}}' 2>/dev/null))"
}

# stage <dind> <local-file> <remote-path> — exec+stdin transport, size and
# (when available) sha256 verified; CR stripped from text scripts afterwards
# (busybox ash cannot run CRLF; local checkouts may be CRLF).
stage() {
    d=$1
    f=$2
    r=$3
    docker exec -i "$d" sh -c "cat > '$r'" <"$f" || die "staging $r into $d"
    hsz=$(wc -c <"$f" | tr -d ' ')
    gsz=$(docker exec "$d" sh -c "wc -c < '$r'" | tr -d ' ')
    [ "$hsz" = "$gsz" ] || die "size mismatch for $r: host=$hsz dind=$gsz"
    case "$r" in
    *.sh)
        docker exec "$d" sed -i 's/\r$//' "$r" || die "strip CR from $r"
        ;;
    esac
    if command -v sha256sum >/dev/null 2>&1; then
        h1=$(sha256sum "$f" | awk '{print $1}')
        g1=$(docker exec "$d" sha256sum "$r" | awk '{print $1}')
        [ "$h1" = "$g1" ] || die "sha256 mismatch for $r: host=$h1 dind=$g1"
    fi
    case "$r" in
    */probe) docker exec "$d" chmod +x "$r" ;;
    esac
}

build_probe() { # <module-dir-under-root> <output-path>
    command -v go >/dev/null 2>&1 ||
        die "go not on PATH (needed to build the spike probe for this suite)"
    log "cross-compiling probe from $1"
    # go.exe is a native Windows binary on the local Git Bash host: hand it a
    # Windows path (MSYS_NO_PATHCONV is on, so /tmp/... would silently land
    # in C:\tmp\...). cygpath is absent on Linux CI -> path used as-is.
    out="$2"
    if command -v cygpath >/dev/null 2>&1; then
        out=$(cygpath -w "$2")
    fi
    (
        cd "$ROOT/$1" &&
            GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w" -o "$out" ./cmd/probe
    ) || die "go build probe from $1"
    [ -s "$2" ] || die "probe binary missing after build: $2"
}

run_inner() { # <dind> <script.sh>
    log "run inner $2 on $1"
    docker exec "$1" sh "/tmp/$2"
}

# ---------------------------------------------------------------- v1/v3/v4
suite_b() { # <name>  (single dind, spike/b-style infra)
    name=$1
    log "===== suite $name: single dind + spike/b infra ====="
    d="fleetly-nightly-$name"
    dind_up "$d"
    stage "$d" "$NLROOT/lib.sh" /tmp/lib.sh
    stage "$d" "$NLROOT/infra-b.sh" /tmp/infra-b.sh
    stage "$d" "$NLROOT/$name.sh" "/tmp/$name.sh"
    build_probe spike/b "$TMP/probe-b"
    docker exec "$d" sh -c 'mkdir -p /tmp/nb-ctx' || die "mkdir nb-ctx"
    # fixture template referenced from spike/b (not copied)
    docker exec -i "$d" sh -c 'cat > /tmp/nb-ctx/Dockerfile' \
        <"$ROOT/spike/b/dockerctx/Dockerfile.fixture" || die "stage Dockerfile.fixture"
    stage "$d" "$TMP/probe-b" /tmp/nb-ctx/probe
    run_inner "$d" infra-b.sh || return 1
    run_inner "$d" "$name.sh"
}

# ---------------------------------------------------------------------- v2
suite_v2() {
    log "===== suite v2: dedicated fresh dind (pristine dockerd log) ====="
    d="fleetly-nightly-v2"
    dind_up "$d"
    stage "$d" "$NLROOT/lib.sh" /tmp/lib.sh
    stage "$d" "$NLROOT/v2.sh" /tmp/v2.sh
    run_inner "$d" v2.sh
    rc=$?
    # host-side pull forensics on the dockerd log (whole-container log is
    # unambiguous here: this dind ran nothing but this test)
    docker logs "$d" >"$TMP/v2-dockerd.log" 2>&1 || true
    DIGEST_PULLS=$(grep -cE 'manifests/sha256' "$TMP/v2-dockerd.log")
    TAG_PULLS=$(grep -cE 'manifests/v1' "$TMP/v2-dockerd.log")
    log "dockerd log: digest-manifest pull lines=$DIGEST_PULLS tag-manifest pull lines=$TAG_PULLS"
    [ "$DIGEST_PULLS" = "0" ]
    assert "V2-ASSERT-3 DOCKERD_LOG_ZERO_DIGEST_PULL" $? "digest pull lines=$DIGEST_PULLS"
    [ "${TAG_PULLS:-0}" -ge 1 ]
    assert "V2-ASSERT-4 TAG_CONTROL_PULL_DETECTED" $? "tag pull lines=$TAG_PULLS (detection channel proof)"
    if [ "$NL_FAIL" -ne 0 ]; then return 1; fi
    return "$rc"
}

# ---------------------------------------------------------------------- v6
suite_v6() {
    log "===== suite v6: dual-dind swarm (mgr + w1 + w2) on $BR_NET ====="
    build_probe spike/c "$TMP/probe-c"
    docker network create -d bridge --subnet "$BR_SUBNET" "$BR_NET" >/dev/null || die "create $BR_NET"
    ACTIVE_NET="$BR_NET"
    MGR=fleetly-nightly-v6-mgr
    W1=fleetly-nightly-v6-w1
    W2=fleetly-nightly-v6-w2
    dind_up "$MGR" --hostname mgr --network "$BR_NET" --ip 10.213.0.10
    dind_up "$W1" --hostname w1 --network "$BR_NET" --ip 10.213.0.11
    dind_up "$W2" --hostname w2 --network "$BR_NET" --ip 10.213.0.12
    for n in "$MGR" "$W1" "$W2"; do
        stage "$n" "$TMP/probe-c" /opt/probe
        stage "$n" "$NLROOT/n-waitsvc.sh" /opt/waitsvc.sh
        stage "$n" "$NLROOT/n-stamp.sh" /opt/stamp.sh
        log "pre-pull alpine:3.20 on $n"
        docker exec "$n" docker pull alpine:3.20 >/dev/null || die "alpine pull on $n"
    done

    log "swarm init on mgr + join w1/w2 + identity labels"
    docker exec "$MGR" docker swarm init --advertise-addr eth0 >/dev/null || die "swarm init"
    JTOK=$(docker exec "$MGR" docker swarm join-token -q worker) || die "join-token"
    docker exec "$W1" docker swarm join 10.213.0.10:2377 --token "$JTOK" >/dev/null || die "w1 join"
    docker exec "$W2" docker swarm join 10.213.0.10:2377 --token "$JTOK" >/dev/null || die "w2 join"
    docker exec "$MGR" docker node update --label-add fleetly.node-id=mgr mgr
    docker exec "$MGR" docker node update --label-add fleetly.node-id=w1 w1
    docker exec "$MGR" docker node update --label-add fleetly.node-id=w2 w2
    i=0
    while :; do
        if docker exec "$MGR" docker node ls --format '{{.Hostname}}={{.Status}}' 2>/dev/null |
            grep -q '^mgr=Ready' &&
            docker exec "$MGR" docker node ls --format '{{.Hostname}}={{.Status}}' 2>/dev/null |
                grep -q '^w1=Ready' &&
            docker exec "$MGR" docker node ls --format '{{.Hostname}}={{.Status}}' 2>/dev/null |
                grep -q '^w2=Ready'; then
            break
        fi
        i=$((i + 2))
        if [ "$i" -ge 60 ]; then
            docker exec "$MGR" docker node ls || true
            die "cluster not Ready within 60s"
        fi
        sleep 2
    done
    docker exec "$MGR" docker node ls

    NL_ROOT="$ROOT" NL_MGR="$MGR" NL_W1="$W1" NL_W2="$W2" NL_ART="$TMP/artifacts" \
        sh "$NLROOT/v6.sh"
}

# ------------------------------------------------------------------- main
[ $# -ge 1 ] || usage
SUITES=""
for a in "$@"; do
    case "$a" in
    all) SUITES="$SUITES v1 v2 v3 v4 v6" ;;
    v1 | v2 | v3 | v4 | v6) SUITES="$SUITES $a" ;;
    *) usage ;;
    esac
done

sweep
command -v docker >/dev/null 2>&1 || die "docker not on PATH"
log "suites:$SUITES  dind=$DIND_IMAGE  extra-args=[$DIND_EXTRA_ARGS]  tmp=$TMP"

for s in $SUITES; do
    # shellcheck disable=SC2034
    NL_FAIL=0
    case "$s" in
    v1 | v3 | v4) suite_b "$s" ;;
    v2) suite_v2 ;;
    v6) suite_v6 ;;
    esac
    rc=$?
    if [ "$rc" -eq 0 ]; then
        PASSED_SUITES="$PASSED_SUITES $s"
        log "suite $s: GREEN"
    else
        FAILED_SUITES="$FAILED_SUITES $s"
        log "suite $s: RED (rc=$rc) — dumping dind logs before cleanup"
        dump_and_forget
    fi
    suite_cleanup
done

log "NIGHTLY-SUMMARY passed:[$PASSED_SUITES ] failed:[$FAILED_SUITES ]"
[ -z "$FAILED_SUITES" ] || exit 1
exit 0
