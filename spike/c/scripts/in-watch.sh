#!/bin/sh
# Generic in-dind timeline watcher. Appends one line per second to
# /work-src/artifacts/logs/<name>.jsonl until <dur> elapses or the stop file
# appears. At the end prints an ANALYSIS block computing millisecond deltas
# against milestone files written by the host orchestrator:
#   <name>.kill   raw RFC3339Nano from `docker inspect <dind> .State.FinishedAt`
#                 taken right after the host killed the node dind
#   <name>.restart raw RFC3339Nano from .State.StartedAt taken right after
#                 the host restarted the node dind
#   <name>.drain / <name>.active  raw unix-ms from `probe ts` taken right
#                 after `docker node update --availability drain|active`
# usage: in-watch.sh <name> <dur-s> <side:mgr|node> [svc] [watched-node-hostname] [interval-s]
NAME=$1; DUR=$2; SIDE=$3; SVC=$4; NODE=$5; INTV=${6:-1}
D=/work-src/artifacts/logs
L=$D/$NAME.jsonl
P=/opt/probe
rm -f "$L" "$D/$NAME.stop" "$D/$NAME.done"
echo "WATCHER-START name=$NAME side=$SIDE svc=$SVC node=$NODE ms=$($P ts)" >> "$L"
END=$(( $(date +%s) + DUR ))
while [ "$(date +%s)" -lt "$END" ]; do
  if [ -f "$D/$NAME.stop" ]; then break; fi
  T=$($P ts)
  if [ "$SIDE" = "mgr" ]; then
    echo "$T nodes $(docker node ls --format '{{.Hostname}}={{.Status}}/{{.Availability}}' 2>&1 | tr '\n' ' ')" >> "$L"
    if [ -n "$SVC" ]; then
      # one line per task (same ts prefix) so per-task greps stay unambiguous
      docker service ps $SVC --format '{{.Name}}|{{.Node}}|{{.CurrentState}}|{{.DesiredState}}' 2>&1 | sed "s/^/$T svc /" >> "$L"
    fi
  else
    echo "$T ps $(docker ps --format '{{.ID}}|{{.Names}}|{{.Status}}' | tr '\n' '~')" >> "$L"
    echo "$T info $(docker info --format '{{.Swarm.LocalNodeState}}/{{.Swarm.ControlAvailable}}' 2>&1)" >> "$L"
  fi
  sleep "$INTV"
done

# ---------- analysis ----------
ms_of() { # ms_of <file> : convert milestone file (RFC3339Nano or plain ms) to ms
  [ -f "$D/$1" ] || { echo "none"; return; }
  V=$(tr -d '\r\n' < "$D/$1")
  case "$V" in
    ''|none) echo "none" ;;
    *[!0-9]*) $P parse -ts "$V" 2>/dev/null || echo none ;;
    *) echo "$V" ;;
  esac
}
first_after() { # first_after <pattern> <ms> : first logged line matching pattern after ms
  grep -F -- "$1" "$L" | awk -v k="$2" '$1 > k' | head -1 | cut -d' ' -f1
}
{
echo "===== ANALYSIS name=$NAME ====="
K=$(ms_of "$NAME.kill");      echo "milestone kill-ms:    $K"
R=$(ms_of "$NAME.restart");   echo "milestone restart-ms: $R"
DR=$(ms_of "$NAME.drain");    echo "milestone drain-ms:   $DR"
AC=$(ms_of "$NAME.active");   echo "milestone active-ms:  $AC"
if [ "$SIDE" = "mgr" ] && [ -n "$NODE" ]; then
  if [ "$K" != "none" ]; then
    DOWN=$(first_after "$NODE=Down" "$K")
    echo "down-at-ms: $DOWN"
    if [ "$DOWN" != "none" ] && [ -n "$DOWN" ]; then echo "delta-kill-to-down-s: $(( (DOWN - K) / 1000 )).$(( ((DOWN - K) % 1000) / 100 ))"; fi
    NEW=$(grep " svc " "$L" | grep -F "|$SVC." | grep -vF "|$NODE|" | grep "|Running" | awk -v k="$K" '$1 > k' | head -1 | cut -d' ' -f1)
    echo "rescheduled-running-at-ms: $NEW"
    if [ "$NEW" != "none" ] && [ -n "$NEW" ]; then echo "delta-kill-to-new-running-s: $(( (NEW - K) / 1000 )).$(( ((NEW - K) % 1000) / 100 ))"; fi
  fi
  if [ "$R" != "none" ]; then
    READY=$(first_after "$NODE=Ready" "$R")
    echo "node-ready-again-at-ms: $READY"
    if [ "$READY" != "none" ] && [ -n "$READY" ]; then echo "delta-restart-to-ready-s: $(( (READY - R) / 1000 )).$(( ((READY - R) % 1000) / 100 ))"; fi
    BACK=$(grep " svc " "$L" | grep -F "|$NODE|Running" | awk -v k="$R" '$1 > k' | head -1 | cut -d' ' -f1)
    echo "task-back-on-node-at-ms: $BACK"
    if [ "$BACK" != "none" ] && [ -n "$BACK" ]; then echo "delta-restart-to-task-back-s: $(( (BACK - R) / 1000 )).$(( ((BACK - R) % 1000) / 100 ))"; fi
    PEND=$(grep -i "pending" "$L" | awk -v k="$K" '$1 > k && $1 < "'"$R"'"' | head -1 | cut -d' ' -f1)
    echo "pending-observed-at-ms (between kill and restart): $PEND"
  fi
  if [ "$DR" != "none" ]; then
    PD=$(grep -i "pending" "$L" | awk -v k="$DR" '$1 > k' | head -1 | cut -d' ' -f1)
    echo "pending-after-drain-at-ms: $PD"
  fi
  if [ "$AC" != "none" ]; then
    TB=$(grep " svc " "$L" | grep -F "|$NODE|Running" | awk -v k="$AC" '$1 > k' | head -1 | cut -d' ' -f1)
    echo "task-back-after-active-at-ms: $TB"
    if [ "$TB" != "none" ] && [ -n "$TB" ]; then echo "delta-active-to-task-back-s: $(( (TB - AC) / 1000 )).$(( ((TB - AC) % 1000) / 100 ))"; fi
  fi
fi
if [ "$SIDE" = "node" ]; then
  # heuristic: a worker that still reaches a manager logs "active/..."; when
  # every manager is unreachable LocalNodeState flips (pending/...) - record
  # both transitions; raw lines above are the evidence.
  BAD=$(grep " info " "$L" | grep -v " info active/" | awk '{print $1}' | head -1)
  echo "first-info-not-active-at-ms: $BAD"
  if [ -n "$BAD" ]; then
    OK=$(grep " info " "$L" | grep " info active/" | awk -v b="$BAD" '$1 > b' | head -1 | cut -d' ' -f1)
    echo "info-back-to-active-at-ms: $OK"
  fi
fi
} >> "$L"
echo "$NAME-DONE" >> "$L"
