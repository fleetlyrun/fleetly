:: C3a - V6a empty-volume accident. A volume-backed service WITHOUT any
:: placement constraint is forced onto w1 by temporarily draining mgr (no
:: service-level constraint involved). mgr is re-activated, then w1 is killed:
:: the task reschedules to mgr and finds a freshly created EMPTY c3vol there
:: (data does not follow). Before/after stamp read is the data-loss evidence.
:: Run: (repo root)  cmd /c spike\c\scripts\c3a-run.bat
call spike\c\scripts\env-common.bat
cd /d %SPIKE_C_DIR%

echo ===== C3A.1 create volume service WITHOUT constraint; drain mgr to force w1 =====
docker exec %SPIKE_MGR% docker service rm c3-app 2>nul
docker exec %SPIKE_MGR% docker node update --availability drain mgr || exit /b 1
docker exec %SPIKE_MGR% docker service create --name c3-app --replicas 1 --mount type=volume,source=c3vol,target=/data --mount type=bind,source=/opt/probe,target=/probe,readonly alpine:3.20 sleep 31536000 || exit /b 1
docker exec %SPIKE_MGR% sh -c "cp /work-src/scripts/in-waitsvc.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c3-app 1 w1 120"
if errorlevel 1 (echo C3A task never Running on w1 & exit /b 1)
docker exec %SPIKE_MGR% docker node update --availability active mgr || exit /b 1

echo ===== C3A.2 BEFORE: write + read stamp inside the volume on w1 =====
docker exec %SPIKE_W1% docker volume inspect c3vol --format "c3vol-on-w1 CreatedAt={{.CreatedAt}}"
docker exec %SPIKE_W1% sh -c "cp /work-src/scripts/in-stamp.sh /tmp/s.sh && sed -i 's/\r$//' /tmp/s.sh && sh /tmp/s.sh c3-app c3vol /data write c3a-initial-worker"
docker exec %SPIKE_W1% sh -c "sh /tmp/s.sh c3-app c3vol /data read"

echo ===== C3A.3 watcher + kill w1 =====
docker exec -d %SPIKE_MGR% sh -c "cp /work-src/scripts/in-watch.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c3a 90 mgr c3-app w1 1"
ping -n 4 127.0.0.1 >nul
rem pre-kill runtime cleanup: stale containerd.pid/docker.pid survive SIGKILL
rem in the container FS and can deterministically break the next dockerd boot
docker exec %SPIKE_W1% sh -c "rm -f /run/docker/containerd/containerd.pid /run/docker/containerd/containerd.sock /var/run/docker.pid" 2>nul
docker kill %SPIKE_W1% || exit /b 1
ping -n 2 127.0.0.1 >nul
for /f "delims=" %%t in ('docker inspect %SPIKE_W1% --format "{{.State.FinishedAt}}"') do set "FIN=%%t"
>artifacts\logs\c3a.kill echo %FIN%
echo kill done, FinishedAt=%FIN%

echo ===== C3A.4 wait watcher done =====
set /a TRY=0
:waitdone
findstr /C:"c3a-DONE" artifacts\logs\c3a.jsonl >nul && goto done
set /a TRY+=1
if %TRY% GEQ 110 (echo watcher did not finish & goto done)
ping -n 2 127.0.0.1 >nul
goto waitdone
:done
type artifacts\logs\c3a.jsonl | findstr /C:"ANALYSIS" /C:"milestone" /C:"delta" /C:"-at-ms" /C:"DONE"

echo ===== C3A.5 AFTER: task rescheduled to mgr - read the volume there =====
docker exec %SPIKE_MGR% sh -c "cp /work-src/scripts/in-waitsvc.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c3-app 1 mgr 120"
docker exec %SPIKE_MGR% docker volume inspect c3vol --format "c3vol-on-mgr CreatedAt={{.CreatedAt}}"
echo --- stamp read on mgr (expect STAMP-MISS = DATA LOSS) ---
docker exec %SPIKE_MGR% sh -c "cp /work-src/scripts/in-stamp.sh /tmp/s.sh && sed -i 's/\r$//' /tmp/s.sh && sh /tmp/s.sh c3-app c3vol /data read"
echo --- marker: expect rc=3 above ---

echo ===== C3A.6 restart w1; original volume still holds the data =====
docker start %SPIKE_W1% || exit /b 1
set /a TRY=0
:waitw1
docker exec %SPIKE_W1% docker info >nul 2>&1
if not errorlevel 1 goto w1up
set /a TRY+=1
if %TRY% GEQ 120 (echo w1 dockerd not back & exit /b 1)
ping -n 3 127.0.0.1 >nul
goto waitw1
:w1up
ping -n 3 127.0.0.1 >nul
docker exec %SPIKE_W1% docker volume inspect c3vol --format "c3vol-on-w1 CreatedAt={{.CreatedAt}} (must differ from mgr volume)"
echo --- helper read of w1 volume (no task there now): original stamp must survive ---
docker exec %SPIKE_W1% sh -c "sh /tmp/s.sh c3-app c3vol /data read-helper"
echo --- service stays on mgr after w1 returns (no rebalance) ---
docker exec %SPIKE_MGR% docker service ps c3-app --format "{{.Name}} {{.Node}} {{.CurrentState}}"

echo ===== C3A.7 service rm does NOT delete volumes =====
docker exec %SPIKE_MGR% docker service rm c3-app
ping -n 3 127.0.0.1 >nul
echo --- volume ls on mgr (c3vol remains = orphan) ---
docker exec %SPIKE_MGR% docker volume ls
echo --- volume ls on w1 (c3vol remains with data) ---
docker exec %SPIKE_W1% docker volume ls
echo ===== C3A done =====
