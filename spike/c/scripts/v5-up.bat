:: V5-UP: build the V5/V5b two-node cluster (fresh dinds, manager static IP
:: 10.10.0.20 so the restored manager can reclaim the exact advertise address)
:: and the service set used by the drill:
::   c5-app-worker  stateless, pinned to worker  (the app that must NOT die)
::   c5-app-mgr     stateless, forced onto manager via drain trick (dies with
::                  the manager - the contrast arm)
::   c6-old         stateless+volume c6vol, pinned to worker (present in the
::                  backup; deleted AFTER the backup; resurrected by rollback)
:: Run: (repo root)  cmd /c spike\c\scripts\v5-up.bat
call spike\c\scripts\env-common.bat
cd /d %SPIKE_C_DIR%

echo ===== V5UP.1 teardown phase-2 leftovers =====
docker rm -f %SPIKE_MGR% %SPIKE_W1% %SPIKE_W2% 2>nul

echo ===== V5UP.2 boot v5m + v5w =====
docker rm -f %SPIKE_V5M% %SPIKE_V5W% %SPIKE_V5M2% 2>nul
docker run -d --name %SPIKE_V5M% --hostname v5m --privileged --network %SPIKE_NET% --ip %SPIKE_IP_V5M% -v "%SPIKE_C_DIR%:/work-src" %SPIKE_DIND_IMAGE% || exit /b 1
docker run -d --name %SPIKE_V5W% --hostname v5w --privileged --network %SPIKE_NET% --ip %SPIKE_IP_V5W% -v "%SPIKE_C_DIR%:/work-src" %SPIKE_DIND_IMAGE% || exit /b 1
set /a TRY=0
:waitm
docker exec %SPIKE_V5M% docker info >nul 2>&1
if not errorlevel 1 goto mup
set /a TRY+=1
if %TRY% GEQ 60 (echo v5m dockerd not ready & exit /b 1)
ping -n 2 127.0.0.1 >nul
goto waitm
:mup
set /a TRY=0
:waitw
docker exec %SPIKE_V5W% docker info >nul 2>&1
if not errorlevel 1 goto wup
set /a TRY+=1
if %TRY% GEQ 60 (echo v5w dockerd not ready & exit /b 1)
ping -n 2 127.0.0.1 >nul
goto waitw
:wup
docker exec %SPIKE_V5M% sh -c "cp /work-src/dockerctx/probe /opt/probe && chmod +x /opt/probe" || exit /b 1
docker exec %SPIKE_V5W% sh -c "cp /work-src/dockerctx/probe /opt/probe && chmod +x /opt/probe" || exit /b 1
docker exec %SPIKE_V5M% docker pull alpine:3.20 1>nul 2>&1
docker exec %SPIKE_V5W% docker pull alpine:3.20 1>nul 2>&1

echo ===== V5UP.3 swarm init + join + labels =====
docker exec %SPIKE_V5M% docker swarm init --advertise-addr eth0 || exit /b 1
for /f %%t in ('docker exec %SPIKE_V5M% docker swarm join-token -q worker') do set "JTOK=%%t"
docker exec %SPIKE_V5W% docker swarm join %SPIKE_IP_V5M%:2377 --token %JTOK% || exit /b 1
docker exec %SPIKE_V5M% docker node update --label-add fleetly.node-id=v5m v5m || exit /b 1
docker exec %SPIKE_V5M% docker node update --label-add fleetly.node-id=v5w v5w || exit /b 1
docker exec %SPIKE_V5M% docker node ls

echo ===== V5UP.4 c5-app-worker pinned to worker =====
docker exec %SPIKE_V5M% docker service create --name c5-app-worker --replicas 1 --constraint node.role==worker alpine:3.20 sleep 31536000 || exit /b 1
docker exec %SPIKE_V5M% sh -c "cp /work-src/scripts/in-waitsvc.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c5-app-worker 1 v5w 120" || exit /b 1

echo ===== V5UP.5 c5-app-mgr forced onto manager (drain trick, no constraint) =====
docker exec %SPIKE_V5M% docker node update --availability drain v5w || exit /b 1
docker exec %SPIKE_V5M% docker service create --name c5-app-mgr --replicas 1 alpine:3.20 sleep 31536000 || exit /b 1
docker exec %SPIKE_V5M% sh -c "sh /tmp/w.sh c5-app-mgr 1 v5m 120" || exit /b 1
docker exec %SPIKE_V5M% docker node update --availability active v5w || exit /b 1

echo ===== V5UP.6 c6-old (volume service on worker; will be deleted after backup) =====
docker exec %SPIKE_V5M% docker service create --name c6-old --replicas 1 --constraint node.role==worker --mount type=volume,source=c6vol,target=/data --mount type=bind,source=/opt/probe,target=/probe,readonly alpine:3.20 sleep 31536000 || exit /b 1
docker exec %SPIKE_V5M% sh -c "sh /tmp/w.sh c6-old 1 v5w 120" || exit /b 1
docker exec %SPIKE_V5W% sh -c "cp /work-src/scripts/in-stamp.sh /tmp/s.sh && sed -i 's/\r$//' /tmp/s.sh && sh /tmp/s.sh c6-old c6vol /data write c6-original-on-v5w"
docker exec %SPIKE_V5W% sh -c "sh /tmp/s.sh c6-old c6vol /data read"

echo ===== V5UP.7 baseline snapshots =====
docker exec %SPIKE_V5M% docker service ls
docker exec %SPIKE_V5M% docker service ps c5-app-worker --format "{{.ID}} {{.Name}} {{.Node}} {{.CurrentState}}" > artifacts\logs\v5-taskids-before.txt
type artifacts\logs\v5-taskids-before.txt
docker exec %SPIKE_V5W% docker ps --format "{{.ID}} {{.Names}} {{.Status}}" > artifacts\logs\v5-containers-before.txt
type artifacts\logs\v5-containers-before.txt
echo ===== V5-UP done =====
