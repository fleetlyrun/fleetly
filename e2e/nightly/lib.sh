#!/bin/sh
# e2e/nightly — shared library for the in-dind regression scripts (and for
# the host-side v6.sh, which only uses the generic sh helpers).
#
# Derived from spike/b/scripts/in-b1-v1b2.sh helper section (wait_healthy /
# wait_img), keeping the restart-churn caveat recorded in spike/b README §9#3:
# a paused service keeps crash-looping its failed task, so `service ps`
# head -1 can be a transient row of the failing version — poll by
# "specific image AND Running", never by head -1.
#
# Assertion style mirrors the spikes: `NAME: PASS` / `NAME: FAIL <detail>`
# lines; NL_FAIL counts failures; finish() exits non-zero if any failed.
#
# shellcheck disable=SC2034
NL_FAIL=0

nl() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
pass() { nl "$1: PASS"; }
fail() { nl "$1: FAIL ${2:-}"; NL_FAIL=$((NL_FAIL + 1)); }
# assert <name> <0|1> [detail]
assert() {
    if [ "$2" -eq 0 ]; then pass "$1"; else fail "$1" "${3:-}"; fi
}
fatal() {
    nl "FATAL $*"
    exit 1
}
finish() {
    if [ "$NL_FAIL" -eq 0 ]; then
        nl "SUITE-DONE all-asserts-passed"
        exit 0
    fi
    nl "SUITE-DONE failed-asserts=$NL_FAIL"
    exit 1
}

# wait_run <svc> <cap_s> — first task of the service reaches Running.
wait_run() {
    i=0
    while [ "$i" -lt "$2" ]; do
        st=$(docker service ps "$1" --format '{{.CurrentState}}' 2>/dev/null | head -1)
        case "$st" in
        Running*) return 0 ;;
        esac
        i=$((i + 1))
        sleep 1
    done
    return 1
}

# wait_img <svc> <image> <cap_s> — a task of exactly this image is Running.
wait_img() {
    i=0
    while [ "$i" -lt "$3" ]; do
        if docker service ps "$1" --format '{{.Image}} {{.CurrentState}}' 2>/dev/null | grep -q "^$2 Running"; then
            return 0
        fi
        i=$((i + 1))
        sleep 1
    done
    return 1
}

# wait_container_gone <ctr> <cap_s> — container left the Running state.
wait_container_gone() {
    i=0
    while [ "$i" -lt "$2" ]; do
        r=$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)
        [ "$r" = "false" ] && return 0
        i=$((i + 1))
        sleep 1
    done
    return 1
}

# grep_count <pattern> <file> — count of matching lines, 0 when none
# (busybox grep -c exits 1 on zero matches; neutralize under set -e-less sh).
grep_count() {
    grep -c "$1" "$2" 2>/dev/null || true
}
