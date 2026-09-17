:: C2 - stateless reschedule on node DOWN. Two replicas spread over mgr+w1;
:: host kills the w1 dind container (node power cut). Timeline: node ls
:: Ready->Down vs the 15-16.5s heartbeat hypothesis; replacement task Running
:: on mgr; replicas back to 2/2. Then w1 restarts and we record that swarm
:: does NOT rebalance back.
:: Run: (repo root)  cmd /c spike\c\scripts\c2-run.bat
call spike\c\scripts\env-common.bat
cd /d %SPIKE_C_DIR%

echo ===== C2.1 create stateless service c2-app replicas=2 =====
docker exec %SPIKE_MGR% docker service rm c2-app 2>nul
docker exec %SPIKE_MGR% docker service create --name c2-app --replicas 2 alpine:3.20 sleep 31536000 || exit /b 1
docker exec %SPIKE_MGR% sh -c "cp /work-src/scripts/in-waitsvc.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c2-app 2 '' 90"
if errorlevel 1 (echo C2 baseline tasks never went Running & exit /b 1)
echo --- placement before kill (expect 1 task per node) ---
docker exec %SPIKE_MGR% docker service ps c2-app --format "{{.Name}} {{.Node}} {{.CurrentState}}"

echo ===== C2.2 start mgr watcher (95s window, 1s sampling) =====
docker exec -d %SPIKE_MGR% sh -c "cp /work-src/scripts/in-watch.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh c2 95 mgr c2-app w1 1"
ping -n 4 127.0.0.1 >nul

echo ===== C2.3 KILL node w1 (host docker kill = clean power cut) =====
docker kill %SPIKE_W1% || exit /b 1
ping -n 2 127.0.0.1 >nul
for /f "delims=" %%t in ('docker inspect %SPIKE_W1% --format "{{.State.FinishedAt}}"') do set "FIN=%%t"
>artifacts\logs\c2.kill echo %FIN%
echo kill done, FinishedAt=%FIN%

echo ===== C2.4 wait watcher done (cap 120s) =====
set /a TRY=0
:waitdone
findstr /C:"c2-DONE" artifacts\logs\c2.jsonl >nul && goto done
set /a TRY+=1
if %TRY% GEQ 120 (echo watcher did not finish & goto done)
ping -n 2 127.0.0.1 >nul
goto waitdone
:done
echo ----- c2.jsonl (tail 60 + analysis) -----
powershell -NoProfile -Command "Get-Content 'artifacts\logs\c2.jsonl' -Tail 60"
type artifacts\logs\c2.jsonl | findstr /C:"ANALYSIS" /C:"milestone" /C:"delta" /C:"-at-ms" /C:"DONE"

echo ===== C2.5 post-kill state: replicas restored? =====
docker exec %SPIKE_MGR% docker service ls
docker exec %SPIKE_MGR% docker service ps c2-app

echo ===== C2.6 restart w1, record no-auto-rebalance =====
docker start %SPIKE_W1% || exit /b 1
set /a TRY=0
:waitw1
docker exec %SPIKE_W1% docker info >nul 2>&1
if not errorlevel 1 goto w1up
set /a TRY+=1
if %TRY% GEQ 90 (echo w1 dockerd not back & exit /b 1)
ping -n 2 127.0.0.1 >nul
goto waitw1
:w1up
ping -n 3 127.0.0.1 >nul
echo --- node ls after w1 back (expect w1 Ready but NO task moved back) ---
docker exec %SPIKE_MGR% docker node ls
docker exec %SPIKE_MGR% docker service ps c2-app --format "{{.Name}} {{.Node}} {{.CurrentState}}"
echo ===== C2.7 cleanup service =====
docker exec %SPIKE_MGR% docker service rm c2-app
echo ===== C2 done =====
