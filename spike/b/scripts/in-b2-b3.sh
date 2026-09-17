#!/bin/sh
# Spike B experiment 2: B3 (TOP PRIORITY) - when does a task join the LB
# endpoint set relative to its health state, in the start-first update
# window. Three sub-runs:
#   B3a healthy slow-start update (v1 -> v2slow: listens after 6s, healthy
#       ~7s): endpoint entry time vs health_status:healthy event.
#   B3b failing update (v1 -> v2bad: /health always 503): does the never-
#       healthy task ever serve traffic / join endpoints -> the "failure =
#       no traffic switch" distortion measurement.
#   B3c no-healthcheck update (v1 -> v2slow, service WITHOUT healthcheck):
#       quantifies the health_gate=none degradation window.
# Evidence: lbwatch JSONL (VIP HTTP samples + tasks.<svc> DNSRR set changes
# at 100ms resolution, same kernel clock) + docker events (task/container
# health_status transitions, nanosecond timestamps).
set -u
say() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
NET=spike-b-net
psfmt='{{.ID}} {{.Name}} {{.Image}} {{.CurrentState}} {{.DesiredState}}'

wait_healthy() { # svc timeout_s
  svc=$1; i=0
  while [ $i -lt $2 ]; do
    st=$(docker service ps "$svc" --format '{{.CurrentState}}' 2>/dev/null | head -1)
    case "$st" in Running*) return 0;; esac
    i=$((i+1)); sleep 1
  done
  return 1
}
start_events() { # file
  docker events --filter type=task --filter type=container > "$1" 2>&1 &
  EV_PID=$!
  sleep 1
}
stop_events() { # file
  kill $EV_PID 2>/dev/null
  wait $EV_PID 2>/dev/null
  say "events captured: $(grep -c . "$1") lines"
}
wait_prober() { # container timeout_s
  i=0
  while [ $i -lt $2 ]; do
    R=$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)
    [ "$R" = "false" ] && return 0
    i=$((i+1)); sleep 1
  done
  say "WARN prober $1 still running after timeout"
}
events_digest() { # file - task transitions + health_status only (drop exec_start noise)
  say "--- task events:"
  awk '$2=="task"' "$1" | head -40
  say "--- health_status events:"
  grep 'health_status' "$1" | head -20
  say "--- container create/die events:"
  awk '$2=="container" && ($3=="create" || $3=="die" || $3=="start")' "$1" | head -20
}
first_seen() { # containerlog version -> ms of first sighting (from summary)
  docker logs "$1" 2>&1 | grep '"ev":"lbwatch-summary"' | grep -o "\"$2\":[0-9]*" | head -1 | cut -d: -f2
}
healthy_event_ms() { # eventsfile containername-substr -> ms of first healthy event
  grep 'health_status: healthy' "$1" | grep "$2" | head -1 | awk '{print $1}' | tr -d '\r' > /tmp/ts.iso
  T=$(cat /tmp/ts.iso)
  [ -n "$T" ] && docker run --rm --entrypoint /probe spike-b/app:v1 ts "$T"
}

#######################################
say "############ B3a: healthy slow-start update ############"
docker service rm app-b3 >/dev/null 2>&1 || true
docker rm -f lbw-b3a >/dev/null 2>&1 || true
sleep 2
docker service create --name app-b3 --network $NET \
  --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
  --health-interval 1s --health-timeout 1s --health-retries 3 --health-start-period 15s \
  --update-failure-action pause --update-order start-first --update-monitor 5s \
  spike-b/app:v1 >/dev/null || exit 1
wait_healthy app-b3 40 || { say "FATAL app-b3 baseline"; exit 1; }
OLD3A=$(docker service ps app-b3 --format '{{.ID}}' | head -1)
say "baseline old task=$OLD3A"
start_events /tmp/b3a.events
docker run -d --name lbw-b3a --network $NET --entrypoint /probe spike-b/app:v1 \
  lbwatch -svc app-b3 -port 8080 -rate-ms 100 -dns-ms 100 -dur 55s >/dev/null
sleep 3
say "T0: issuing update v1 -> v2slow (listens after 6s; start-period 15s)"
docker service update --image spike-b/app:v2slow app-b3 > /tmp/b3a.update.out 2>&1
say "update exit=$? output:"
cat /tmp/b3a.update.out
wait_healthy app-b3 40 || say "WARN app-b3 not healthy in 40s"
docker service ps app-b3 --format "$psfmt" --no-trunc
stop_events /tmp/b3a.events
wait_prober lbw-b3a 70
say "--- B3a lbwatch summary:"
docker logs lbw-b3a 2>&1 | grep '"ev":"lbwatch-summary"'
say "--- B3a dnsrr transitions:"
docker logs lbw-b3a 2>&1 | grep '"ev":"dnsrr"'
say "--- B3a version sightings:"
docker logs lbw-b3a 2>&1 | grep '"first_seen":true'
say "--- B3a failures: $(docker logs lbw-b3a 2>&1 | grep -c '"fail"')"
events_digest /tmp/b3a.events
HEALTHY_MS=$(healthy_event_ms /tmp/b3a.events app-b3)
V2_MS=$(first_seen lbw-b3a v2)
if [ -n "$HEALTHY_MS" ] && [ -n "$V2_MS" ]; then
  say "B3a-RESULT: first-v2-through-VIP ms=$V2_MS ; healthy-event ms=$HEALTHY_MS ; delta=$((V2_MS-HEALTHY_MS)) ms"
else
  say "B3a-RESULT: missing timestamps healthy=$HEALTHY_MS v2=$V2_MS"
fi
docker rm -f lbw-b3a >/dev/null 2>&1 || true

#######################################
say "############ B3b: failing (never-healthy) update ############"
docker service rm app-b3f >/dev/null 2>&1 || true
docker rm -f lbw-b3b >/dev/null 2>&1 || true
sleep 2
docker service create --name app-b3f --network $NET \
  --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
  --health-interval 1s --health-timeout 1s --health-retries 2 --health-start-period 5s \
  --update-failure-action pause --update-order start-first --update-monitor 5s \
  spike-b/app:v1 >/dev/null || exit 1
wait_healthy app-b3f 40 || { say "FATAL app-b3f baseline"; exit 1; }
OLD3B=$(docker service ps app-b3f --format '{{.ID}}' | head -1)
say "baseline old task=$OLD3B"
start_events /tmp/b3b.events
docker run -d --name lbw-b3b --network $NET --entrypoint /probe spike-b/app:v1 \
  lbwatch -svc app-b3f -port 8080 -rate-ms 100 -dns-ms 100 -dur 45s >/dev/null
sleep 3
say "T0: issuing update v1 -> v2bad (serves / but /health always 503)"
docker service update --image spike-b/app:v2bad app-b3f > /tmp/b3b.update.out 2>&1
say "update exit=$? output:"
cat /tmp/b3b.update.out
sleep 15
docker service ps app-b3f --format "$psfmt" --no-trunc
docker service inspect --format 'UpdateStatus={{json .UpdateStatus}}' app-b3f
stop_events /tmp/b3b.events
wait_prober lbw-b3b 60
say "--- B3b lbwatch summary:"
docker logs lbw-b3b 2>&1 | grep '"ev":"lbwatch-summary"'
say "--- B3b dnsrr transitions:"
docker logs lbw-b3b 2>&1 | grep '"ev":"dnsrr"'
say "--- B3b v2bad sighting samples (distortion evidence):"
docker logs lbw-b3b 2>&1 | grep '"ver":"v2bad"' | head -10
say "--- B3b failure samples:"
docker logs lbw-b3b 2>&1 | grep '"fail"' | head -10
say "--- B3b failure count: $(docker logs lbw-b3b 2>&1 | grep -c '"fail"')"
events_digest /tmp/b3b.events
docker rm -f lbw-b3b >/dev/null 2>&1 || true

#######################################
say "############ B3c: no-healthcheck update (health_gate=none degradation) ############"
docker service rm app-b3n >/dev/null 2>&1 || true
docker rm -f lbw-b3c >/dev/null 2>&1 || true
sleep 2
docker service create --name app-b3n --network $NET \
  --update-failure-action pause --update-order start-first --update-monitor 5s \
  spike-b/app:v1 >/dev/null || exit 1
wait_healthy app-b3n 30 || { say "FATAL app-b3n baseline"; exit 1; }
OLD3C=$(docker service ps app-b3n --format '{{.ID}}' | head -1)
say "baseline old task=$OLD3C"
start_events /tmp/b3c.events
docker run -d --name lbw-b3c --network $NET --entrypoint /probe spike-b/app:v1 \
  lbwatch -svc app-b3n -port 8080 -rate-ms 200 -dns-ms 200 -dur 40s >/dev/null
sleep 3
say "T0: issuing update v1 -> v2slow with NO healthcheck (task RUNNING at container start)"
docker service update --image spike-b/app:v2slow app-b3n > /tmp/b3c.update.out 2>&1
say "update exit=$? output:"
cat /tmp/b3c.update.out
wait_healthy app-b3n 40 || say "WARN app-b3n not healthy in 40s"
stop_events /tmp/b3c.events
wait_prober lbw-b3c 60
say "--- B3c lbwatch summary:"
docker logs lbw-b3c 2>&1 | grep '"ev":"lbwatch-summary"'
say "--- B3c dnsrr transitions:"
docker logs lbw-b3c 2>&1 | grep '"ev":"dnsrr"'
say "--- B3c v2 first sighting:"
docker logs lbw-b3c 2>&1 | grep '"first_seen":true'
say "--- B3c failure samples (expect the un-listening new task window):"
docker logs lbw-b3c 2>&1 | grep '"fail"' | head -8
say "--- B3c failure count: $(docker logs lbw-b3c 2>&1 | grep -c '"fail"')"
events_digest /tmp/b3c.events
docker rm -f lbw-b3c >/dev/null 2>&1 || true

say "B2-B3-EXPERIMENT-DONE"
