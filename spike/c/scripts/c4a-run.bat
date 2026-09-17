:: C4a - V6b drain/rebind. Pinned volume service on w1. Drain the bound node:
:: task shuts down, replacement stays PENDING (app blocked), volume data stays
:: on the drained node. Reactivate (active): task automatically rebinds to the
:: same node, data intact.
:: Run: (repo root)  cmd /c spike\c\scripts\c4a-run.bat
call spike\c\scripts\env-common.bat
cd /d %SPIKE_C_DIR%

echo ===== C4A.1 create pinned volume service on w1 =====
docker exec %SPIKE_MGR% docker service rm c4-app 2>nul
docker exec %SPIKE_MGR% docker service create --name c4-app --replicas 1 --constraint node.labels.edgefleet.node-id==w1 --mount type=volume,source=c4vol,target=/data --mount type=bind,source=/opt/probe,target=/probe,readonly alpine:3.20 sleep 31536000 || exit /b 1
docker exec %SPIKE_MGR% sh -c "cp /work-src/scripts/in-waitsvc.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c4-app 1 w1 120"
if errorlevel 1 (echo C4A task never Running on w1 & exit /b 1)

echo ===== C4A.2 BEFORE: write + read stamp =====
docker exec %SPIKE_W1% sh -c "cp /work-src/scripts/in-stamp.sh /tmp/s.sh && sed -i 's/\r$//' /tmp/s.sh && sh /tmp/s.sh c4-app c4vol /data write c4a-initial-worker"
docker exec %SPIKE_W1% sh -c "sh /tmp/s.sh c4-app c4vol /data read"

echo ===== C4A.3 watcher + drain w1 =====
docker exec -d %SPIKE_MGR% sh -c "cp /work-src/scripts/in-watch.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c4a 110 mgr c4-app w1 1"
ping -n 4 127.0.0.1 >nul
docker exec %SPIKE_MGR% docker node update --availability drain w1 || exit /b 1
for /f %%t in ('docker exec %SPIKE_MGR% /opt/probe ts') do >artifacts\logs\c4a.drain echo %%t
echo drain milestone captured (artifacts\logs\c4a.drain)

echo ===== C4A.4 +20s: app blocked semantics =====
ping -n 21 127.0.0.1 >nul
echo --- node ls (w1 Ready/drain) ---
docker exec %SPIKE_MGR% docker node ls
echo --- service ps (expect old Shutdown + new PENDING, nothing Running) ---
docker exec %SPIKE_MGR% docker service ps c4-app
echo --- running count (expect 0) ---
docker exec %SPIKE_MGR% docker service ps c4-app --format "{{.CurrentState}}" | find /c "Run"
echo --- w1 docker ps (task container gone = app down at the node) ---
docker exec %SPIKE_W1% docker ps -a --format "{{.Names}} {{.Status}}"
echo --- volume data still on w1 (helper read, no task container) ---
docker exec %SPIKE_W1% sh -c "sh /tmp/s.sh c4-app c4vol /data read-helper"

echo ===== C4A.5 reactivate (active) -> automatic rebind =====
docker exec %SPIKE_MGR% docker node update --availability active w1 || exit /b 1
for /f %%t in ('docker exec %SPIKE_MGR% /opt/probe ts') do >artifacts\logs\c4a.active echo %%t
docker exec %SPIKE_MGR% sh -c "cp /work-src/scripts/in-waitsvc.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c4-app 1 w1 120"
if errorlevel 1 (echo C4A task never came back after active & goto skip)

echo ===== C4A.6 AFTER: read stamp - data intact =====
docker exec %SPIKE_W1% sh -c "sh /tmp/s.sh c4-app c4vol /data read"
echo --- marker: STAMP-OK same token/ms as C4A.2 = drain roundtrip keeps data ---
:skip

echo ===== C4A.7 watcher analysis + cleanup =====
set /a TRY=0
:waitdone
findstr /C:"c4a-DONE" artifacts\logs\c4a.jsonl >nul && goto done
set /a TRY+=1
if %TRY% GEQ 120 (echo watcher did not finish & goto done)
ping -n 2 127.0.0.1 >nul
goto waitdone
:done
type artifacts\logs\c4a.jsonl | findstr /C:"ANALYSIS" /C:"milestone" /C:"delta" /C:"-at-ms" /C:"DONE"
docker exec %SPIKE_MGR% docker service rm c4-app
echo ===== C4A done =====
