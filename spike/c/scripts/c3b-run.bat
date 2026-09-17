:: C3b - V6a pinned semantics. Same volume service but WITH
:: --constraint node.labels.edgefleet.node-id==w1. Kill w1: task must stay
:: PENDING (no migration, no empty-volume accident). Restart the SAME dind
:: container (docker start; a new dind would have no swarm identity): node
:: rejoins automatically, task returns to w1, volume data intact.
:: Run: (repo root)  cmd /c spike\c\scripts\c3b-run.bat
call spike\c\scripts\env-common.bat
cd /d %SPIKE_C_DIR%

echo ===== C3B.1 create pinned volume service on w1 =====
docker exec %SPIKE_MGR% docker service rm c3b-app 2>nul
docker exec %SPIKE_MGR% docker service create --name c3b-app --replicas 1 --constraint node.labels.edgefleet.node-id==w1 --mount type=volume,source=c3bvol,target=/data --mount type=bind,source=/opt/probe,target=/probe,readonly alpine:3.20 sleep 31536000 || exit /b 1
docker exec %SPIKE_MGR% sh -c "cp /work-src/scripts/in-waitsvc.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c3b-app 1 w1 120"
if errorlevel 1 (echo C3B task never Running on w1 & exit /b 1)
docker exec %SPIKE_W1% docker volume inspect c3bvol --format "c3bvol-on-w1 CreatedAt={{.CreatedAt}}"

echo ===== C3B.2 BEFORE: write + read stamp on w1 =====
docker exec %SPIKE_W1% sh -c "cp /work-src/scripts/in-stamp.sh /tmp/s.sh && sed -i 's/\r$//' /tmp/s.sh && sh /tmp/s.sh c3b-app c3bvol /data write c3b-initial-worker"
docker exec %SPIKE_W1% sh -c "sh /tmp/s.sh c3b-app c3bvol /data read"

echo ===== C3B.3 watcher (360s window covers kill->pending->dockerd recovery->rejoin->back) =====
docker exec -d %SPIKE_MGR% sh -c "cp /work-src/scripts/in-watch.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c3b 360 mgr c3b-app w1 1"
ping -n 4 127.0.0.1 >nul

echo ===== C3B.4 kill w1 =====
rem pre-kill runtime cleanup so the next dockerd boot is deterministic
docker exec %SPIKE_W1% sh -c "rm -f /run/docker/containerd/containerd.pid /run/docker/containerd/containerd.sock /var/run/docker.pid" 2>nul
docker kill %SPIKE_W1% || exit /b 1
ping -n 2 127.0.0.1 >nul
for /f "delims=" %%t in ('docker inspect %SPIKE_W1% --format "{{.State.FinishedAt}}"') do set "FIN=%%t"
>artifacts\logs\c3b.kill echo %FIN%
echo kill done, FinishedAt=%FIN%

echo ===== C3B.5 +25s: node Down, task PENDING, NOTHING on mgr =====
ping -n 26 127.0.0.1 >nul
echo --- node ls ---
docker exec %SPIKE_MGR% docker node ls
echo --- service ps (expect PENDING, no Running anywhere) ---
docker exec %SPIKE_MGR% docker service ps c3b-app
echo --- running-task count on any node (expect 0) ---
docker exec %SPIKE_MGR% docker service ps c3b-app --format "{{.CurrentState}}" | find /c "Run"

echo ===== C3B.6 restart SAME dind container (docker start), capture StartedAt =====
docker start %SPIKE_W1% || exit /b 1
ping -n 2 127.0.0.1 >nul
for /f "delims=" %%t in ('docker inspect %SPIKE_W1% --format "{{.State.StartedAt}}"') do set "STA=%%t"
>artifacts\logs\c3b.restart echo %STA%
echo restart done, StartedAt=%STA%

echo ===== C3B.7 wait task back Running on w1 (cap 300s; dockerd recovery after kill can exceed 90s) =====
set /a TRY=0
:waitback
docker exec %SPIKE_W1% sh -c "docker service ps c3b-app --format '{{.Node}}|{{.CurrentState}}' 2>/dev/null | grep -c '^w1|Running'" 2>nul | findstr /r "[1-9]" >nul && goto back
set /a TRY+=1
if %TRY% GEQ 300 (echo task never came back to w1 & goto back)
ping -n 2 127.0.0.1 >nul
goto waitback
:back
ping -n 3 127.0.0.1 >nul
docker exec %SPIKE_MGR% docker service ps c3b-app

echo ===== C3B.8 AFTER: read stamp on w1 - data must be intact =====
docker exec %SPIKE_W1% docker volume inspect c3bvol --format "c3bvol-on-w1 CreatedAt={{.CreatedAt}} (must equal pre-kill)"
rem /tmp is wiped by the dind restart - restage before use
docker exec %SPIKE_W1% sh -c "cp /work-src/scripts/in-stamp.sh /tmp/s.sh && sed -i 's/\r$//' /tmp/s.sh && sh /tmp/s.sh c3b-app c3bvol /data read"
echo --- marker: STAMP-OK with same ms/token as C3B.2 = data intact ---

echo ===== C3B.9 wait watcher analysis + cleanup =====
set /a TRY=0
:waitdone
findstr /C:"c3b-DONE" artifacts\logs\c3b.jsonl >nul && goto done
set /a TRY+=1
if %TRY% GEQ 380 (echo watcher did not finish & goto done)
ping -n 2 127.0.0.1 >nul
goto waitdone
:done
type artifacts\logs\c3b.jsonl | findstr /C:"ANALYSIS" /C:"milestone" /C:"delta" /C:"-at-ms" /C:"DONE"
docker exec %SPIKE_MGR% docker service rm c3b-app
echo ===== C3B done =====
