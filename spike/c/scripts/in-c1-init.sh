#!/bin/sh
# C1 - swarm init transparency on a fresh single dind.
# Side-effect inventory: plain container + named volume survive `swarm init`
# untouched; docker info flips to active; ingress + docker_gwbridge appear;
# 2377 (mgmt) / 7946 (gossip) / 4789 (vxlan) listeners appear; iptables rules
# and links multiply. Every assert prints PASS/FAIL with raw evidence.
P=/opt/probe
L=/work-src/artifacts/logs/in-c1.log
fail=0
# NOTE: this script is invoked with stdout/stderr already redirected by
# c0-up.bat to the same path as $L - writing to $L from inside would hit the
# Windows file lock (Permission denied), so just echo; the host redirect captures.
note() { echo "$*"; }
assert() { # assert <label> <expected-pass:0|1> <actual-rc-or-count-check already done>
  if [ "$2" = "0" ]; then note "C1-ASSERT-$1: PASS"; else note "C1-ASSERT-$1: FAIL"; fail=1; fi
}

note "===== C1.0 env ====="
note "probe=$(sha256sum /opt/probe | cut -c1-16) kernel=$(uname -r) engine=$(docker version --format '{{.Server.Version}}')"
note "now-ms=$($P ts)"

note "===== C1.1 pre-init state (plain docker run container + named volume) ====="
docker volume rm c1vol >/dev/null 2>&1
docker rm -f c1-ctr >/dev/null 2>&1
docker run -d --name c1-ctr -v c1vol:/data alpine:3.20 \
  sh -c "echo stamp-c1-plaintext > /data/stamp.txt && sleep 31536000" || exit 1
sleep 2
note "-- docker ps (c1-ctr expected Up)"
docker ps --format "{{.Names}} {{.Status}} {{.Image}}" | tee -a $L
note "-- volume readable before init"
docker exec c1-ctr cat /data/stamp.txt | tee -a $L
note "-- network list before"
docker network ls | tee -a $L
note "-- docker info swarm state before: $(docker info --format '{{.Swarm.LocalNodeState}}')"
note "-- listeners before (netstat -lnt)"
netstat -lnt | tee -a $L
note "-- links before: $(ip -o link | wc -l)"
ip -o link | cut -d: -f2 | tr -d ' ' | tr '\n' ',' >> $L; note ""
iptables-save > /tmp/c1-ipt-before.txt
note "-- iptables rule lines before: $(wc -l < /tmp/c1-ipt-before.txt)"
note "-- swarm-managed label on c1-ctr before init (expect <no value>):"
docker inspect -f "{{index .Config.Labels \"com.docker.swarm.task.id\"}}" c1-ctr | tee -a $L

note "===== C1.2 docker swarm init --advertise-addr eth0 ====="
docker swarm init --advertise-addr eth0 2>&1 | tee -a $L

note "===== C1.3 post-init side-effect inventory ====="
sleep 2
note "-- docker ps after (c1-ctr must still be Up)"
docker ps --format "{{.Names}} {{.Status}} {{.Image}}" | tee -a $L
docker ps --format "{{.Names}} {{.Status}}" | grep -q "c1-ctr.*Up"
assert EXISTING_CONTAINER_RUNNING $? 2>/dev/null
note "-- volume data readable after init"
docker exec c1-ctr cat /data/stamp.txt | tee -a $L
[ "$(docker exec c1-ctr cat /data/stamp.txt)" = "stamp-c1-plaintext" ]
assert EXISTING_VOLUME_DATA_INTACT $?
note "-- c1-ctr stays a plain (non-swarm) container; swarm task label expect <no value>:"
docker inspect -f "{{index .Config.Labels \"com.docker.swarm.task.id\"}}" c1-ctr | tee -a $L
note "-- docker info after"
docker info --format "LocalNodeState={{.Swarm.LocalNodeState}} ControlAvailable={{.Swarm.ControlAvailable}} NodeID={{.Swarm.NodeID}}" | tee -a $L
[ "$(docker info --format '{{.Swarm.LocalNodeState}}')" = "active" ]
assert INFO_SWARM_ACTIVE $?
note "-- node ls"
docker node ls | tee -a $L
note "-- network list after (expect +ingress +docker_gwbridge)"
docker network ls | tee -a $L
docker network ls --format "{{.Name}}" | grep -qx ingress; a=$?
docker network ls --format "{{.Name}}" | grep -qx docker_gwbridge; b=$?
[ $a -eq 0 ] && [ $b -eq 0 ]
assert NETWORKS_INGRESS_GWBRIDGE $?
note "-- listeners after (expect :2377 :7946 :4789)"
netstat -lnt | tee -a $L
netstat -lnt | grep -q ":2377"
assert LISTEN_2377 $?
note "-- links after: $(ip -o link | wc -l)  (before logged above)"
ip -o link | cut -d: -f2 | tr -d ' ' | tr '\n' ',' >> $L; note ""
iptables-save > /tmp/c1-ipt-after.txt
note "-- iptables rule lines after: $(wc -l < /tmp/c1-ipt-after.txt)"
note "-- iptables diff (chains/rules added by swarm init, first 40 lines)"
diff /tmp/c1-ipt-before.txt /tmp/c1-ipt-after.txt | head -40 | tee -a $L
note "-- volume list after (c1vol untouched)"
docker volume ls | tee -a $L

if [ $fail -eq 0 ]; then note "C1-OK"; else note "C1-FAILED"; exit 1; fi
