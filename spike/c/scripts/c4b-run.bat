:: C4b - V6b node removal. Pinned volume service on w1. Kill w1, wait Down,
:: `docker node rm` the node: the task must stay PENDING forever (no auto
:: migration). Recovery is manual: boot a NEW worker w2, join, then rebind by
:: swapping the constraint to the new node - the task then runs on w2 with an
:: EMPTY volume (data does not follow; this is why the platform demands a
:: --data-restored ack before rebinding). The old stamp is rescued out of the
:: dead w1 via docker cp from the stopped container.
:: Run: (repo root)  cmd /c spike\c\scripts\c4b-run.bat
call spike\c\scripts\env-common.bat
cd /d %SPIKE_C_DIR%

echo ===== C4B.1 create pinned volume service on w1 =====
docker exec %SPIKE_MGR% docker service rm c4b-app 2>nul
docker exec %SPIKE_MGR% docker service create --name c4b-app --replicas 1 --constraint node.labels.fleetly.node-id==w1 --mount type=volume,source=c4bvol,target=/data --mount type=bind,source=/opt/probe,target=/probe,readonly alpine:3.20 sleep 31536000 || exit /b 1
docker exec %SPIKE_MGR% sh -c "cp /work-src/scripts/in-waitsvc.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c4b-app 1 w1 120"
if errorlevel 1 (echo C4B task never Running on w1 & exit /b 1)
docker exec %SPIKE_W1% sh -c "cp /work-src/scripts/in-stamp.sh /tmp/s.sh && sed -i 's/\r$//' /tmp/s.sh && sh /tmp/s.sh c4b-app c4bvol /data write c4b-initial-worker"
docker exec %SPIKE_W1% sh -c "sh /tmp/s.sh c4b-app c4bvol /data read"

echo ===== C4B.2 watcher + kill w1 =====
docker exec -d %SPIKE_MGR% sh -c "cp /work-src/scripts/in-watch.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c4b 120 mgr c4b-app w1 1"
ping -n 4 127.0.0.1 >nul
docker kill %SPIKE_W1% || exit /b 1
ping -n 2 127.0.0.1 >nul
for /f "delims=" %%t in ('docker inspect %SPIKE_W1% --format "{{.State.FinishedAt}}"') do set "FIN=%%t"
>artifacts\logs\c4b.kill echo %FIN%
echo kill done, FinishedAt=%FIN%

echo ===== C4B.3 wait w1 Down then node rm =====
set /a TRY=0
:waitdown
docker exec %SPIKE_MGR% docker node ls --format "{{.Hostname}}={{.Status}}" | findstr /C:"w1=Down" >nul && goto down
set /a TRY+=1
if %TRY% GEQ 40 (echo w1 never went Down & goto down)
ping -n 2 127.0.0.1 >nul
goto waitdown
:down
docker exec %SPIKE_MGR% docker node ls
echo --- node rm w1 (plain; fallback --force) ---
docker exec %SPIKE_MGR% docker node rm w1
if errorlevel 1 (
  echo plain rm failed, trying --force
  docker exec %SPIKE_MGR% docker node rm --force w1
)
docker exec %SPIKE_MGR% docker node ls

echo ===== C4B.4 PENDING persists (3 snapshots, 20s apart) =====
ping -n 21 127.0.0.1 >nul
docker exec %SPIKE_MGR% docker service ps c4b-app
ping -n 21 127.0.0.1 >nul
docker exec %SPIKE_MGR% docker service ps c4b-app
ping -n 21 127.0.0.1 >nul
docker exec %SPIKE_MGR% docker service ps c4b-app
echo --- running count (expect 0 across all snapshots) ---
docker exec %SPIKE_MGR% docker service ps c4b-app --format "{{.CurrentState}}" | find /c "Run"

echo ===== C4B.5 boot NEW worker w2, join, label =====
docker rm -f %SPIKE_W2% 2>nul
docker run -d --name %SPIKE_W2% --hostname w2 --privileged --network %SPIKE_NET% --ip %SPIKE_IP_W2% -v "%SPIKE_C_DIR%:/work-src" %SPIKE_DIND_IMAGE% || exit /b 1
set /a TRY=0
:waitw2
docker exec %SPIKE_W2% docker info >nul 2>&1
if not errorlevel 1 goto w2up
set /a TRY+=1
if %TRY% GEQ 60 (echo w2 dockerd not ready & exit /b 1)
ping -n 2 127.0.0.1 >nul
goto waitw2
:w2up
docker exec %SPIKE_W2% sh -c "cp /work-src/dockerctx/probe /opt/probe && chmod +x /opt/probe" || exit /b 1
docker exec %SPIKE_W2% docker pull alpine:3.20 1>nul 2>&1
for /f %%t in ('docker exec %SPIKE_MGR% docker swarm join-token -q worker') do set "JTOK=%%t"
docker exec %SPIKE_W2% docker swarm join %SPIKE_IP_MGR%:2377 --token %JTOK% || exit /b 1
docker exec %SPIKE_MGR% docker node update --label-add fleetly.node-id=w2 w2 || exit /b 1
ping -n 3 127.0.0.1 >nul
docker exec %SPIKE_MGR% docker node ls

echo ===== C4B.6 manual rebind: swap constraint to w2 =====
docker exec %SPIKE_MGR% docker service update --constraint-rm node.labels.fleetly.node-id==w1 --constraint-add node.labels.fleetly.node-id==w2 c4b-app || exit /b 1
docker exec %SPIKE_MGR% sh -c "cp /work-src/scripts/in-waitsvc.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c4b-app 1 w2 120"
if errorlevel 1 (echo C4B task never Running on w2 & exit /b 1)

echo ===== C4B.7 AFTER rebind: read volume on w2 - EMPTY (data did not follow) =====
docker exec %SPIKE_W2% sh -c "cp /work-src/scripts/in-stamp.sh /tmp/s.sh && sed -i 's/\r$//' /tmp/s.sh && sh /tmp/s.sh c4b-app c4bvol /data read"
docker exec %SPIKE_W2% docker volume inspect c4bvol --format "c4bvol-on-w2 CreatedAt={{.CreatedAt}} (fresh volume, not the w1 one)"

echo ===== C4B.8 rescue the original stamp out of the dead w1 (docker cp, stopped container) =====
docker cp %SPIKE_W1%:/var/lib/docker/volumes/c4bvol/_data/stamp.json artifacts\logs\c4b-rescued-stamp.json
if errorlevel 1 (echo docker cp from stopped w1 FAILED) else (type artifacts\logs\c4b-rescued-stamp.json)

echo ===== C4B.9 watcher analysis + cleanup =====
set /a TRY=0
:waitdone
findstr /C:"c4b-DONE" artifacts\logs\c4b.jsonl >nul && goto done
set /a TRY+=1
if %TRY% GEQ 130 (echo watcher did not finish & goto done)
ping -n 2 127.0.0.1 >nul
goto waitdone
:done
type artifacts\logs\c4b.jsonl | findstr /C:"ANALYSIS" /C:"milestone" /C:"delta" /C:"-at-ms" /C:"DONE"
docker exec %SPIKE_MGR% docker service rm c4b-app
echo ===== C4B done =====
