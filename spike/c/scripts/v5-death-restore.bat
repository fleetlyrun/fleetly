:: V5-DEATH-RESTORE: the actual V5/V5b drill.
::  (a) mutate AFTER the backup: delete c6-old, create c5-new
::  (b) kill the manager for good (stop + rm; frees IP 10.10.0.20)
::  (c) boot v5m2 on the SAME IP, stage the cold backup, restart dockerd onto
::      it, attempt `docker swarm init --force-new-cluster`, watch the worker
::      reconnect
::  (d) assert: service definitions restored, c5-app-worker task identity
::      continuous (app never interrupted), c6-old resurrected, c5-new fate
::      observed (V5b orphan question), c5-app-mgr rebuilt on v5m2
:: Run: (repo root)  cmd /c spike\c\scripts\v5-death-restore.bat
call spike\c\scripts\env-common.bat
cd /d %SPIKE_C_DIR%

echo ===== V5D.1 mutations AFTER the backup =====
docker exec %SPIKE_V5M% docker service rm c6-old || exit /b 1
ping -n 3 127.0.0.1 >nul
docker exec %SPIKE_V5M% docker service create --name c5-new --replicas 1 --constraint node.role==worker --mount type=volume,source=c5newvol,target=/data --mount type=bind,source=/opt/probe,target=/probe,readonly alpine:3.20 sleep 31536000 || exit /b 1
docker exec %SPIKE_V5W% sh -c "sh /tmp/s.sh c5-new c5newvol /data write c5new-original-after-backup"
docker exec %SPIKE_V5W% sh -c "sh /tmp/s.sh c5-new c5newvol /data read"
echo --- state at death time (truth) ---
docker exec %SPIKE_V5M% docker service ls > artifacts\logs\v5-state-at-death.txt
type artifacts\logs\v5-state-at-death.txt
docker exec %SPIKE_V5W% docker ps --format "{{.ID}} {{.Names}} {{.Status}}" > artifacts\logs\v5-containers-at-death.txt
type artifacts\logs\v5-containers-at-death.txt
docker exec %SPIKE_V5M% docker service ps c5-app-worker --format "{{.ID}} {{.Name}} {{.Node}} {{.CurrentState}}" > artifacts\logs\v5-taskid-at-death.txt
type artifacts\logs\v5-taskid-at-death.txt

echo ===== V5D.2 DEATH: stop + rm manager (never comes back) =====
docker stop -t 30 %SPIKE_V5M% || exit /b 1
ping -n 2 127.0.0.1 >nul
for /f "delims=" %%t in ('docker inspect %SPIKE_V5M% --format "{{.State.FinishedAt}}"') do set "FIN=%%t"
>artifacts\logs\v5death.at echo %FIN%
echo manager dead at %FIN%
docker rm %SPIKE_V5M% || exit /b 1

echo ===== V5D.3 manager-down window: worker keeps serving =====
docker exec %SPIKE_V5W% sh -c "echo ts-ms=$(/opt/probe ts); docker info --format 'info={{.Swarm.LocalNodeState}}/{{.Swarm.ControlAvailable}}'; echo -- docker node ls from worker --; docker node ls 2>&1 | head -2; echo -- containers --; docker ps --format '{{.ID}} {{.Names}} {{.Status}}'" > artifacts\logs\v5-managerdown-1.txt
type artifacts\logs\v5-managerdown-1.txt
ping -n 11 127.0.0.1 >nul
docker exec %SPIKE_V5W% sh -c "echo ts-ms=$(/opt/probe ts); docker ps --format '{{.ID}} {{.Names}} {{.Status}}'" > artifacts\logs\v5-managerdown-2.txt
type artifacts\logs\v5-managerdown-2.txt
ping -n 11 127.0.0.1 >nul
docker exec %SPIKE_V5W% sh -c "echo ts-ms=$(/opt/probe ts); docker ps --format '{{.ID}} {{.Names}} {{.Status}}'" > artifacts\logs\v5-managerdown-3.txt
type artifacts\logs\v5-managerdown-3.txt

echo ===== V5D.4 boot v5m2 on the SAME IP, stage cold backup =====
docker rm -f %SPIKE_V5M2% 2>nul
docker run -d --name %SPIKE_V5M2% --hostname v5m2 --privileged --network %SPIKE_NET% --ip %SPIKE_IP_V5M% -v "%SPIKE_C_DIR%:/work-src" %SPIKE_DIND_IMAGE% || exit /b 1
set /a TRY=0
:waitm2
docker exec %SPIKE_V5M2% docker info >nul 2>&1
if not errorlevel 1 goto m2up
set /a TRY+=1
if %TRY% GEQ 60 (echo v5m2 dockerd not ready & exit /b 1)
ping -n 2 127.0.0.1 >nul
goto waitm2
:m2up
docker exec %SPIKE_V5M2% sh -c "cp /work-src/dockerctx/probe /opt/probe && chmod +x /opt/probe"
docker exec %SPIKE_V5M2% sh -c "cp /work-src/scripts/in-v5-restore.sh /tmp/r.sh && sed -i 's/\r$//' /tmp/r.sh && sh /tmp/r.sh" | tee artifacts\logs\v5-restore-staged.log

echo ===== V5D.5 restart v5m2 so dockerd boots onto the restored state =====
docker stop -t 30 %SPIKE_V5M2% >nul
docker start %SPIKE_V5M2% || exit /b 1
set /a TRY=0
:waitm2b
docker exec %SPIKE_V5M2% docker info >nul 2>&1
if not errorlevel 1 goto m2back
set /a TRY+=1
if %TRY% GEQ 60 (echo v5m2 dockerd not back & exit /b 1)
ping -n 2 127.0.0.1 >nul
goto waitm2b
:m2back
ping -n 5 127.0.0.1 >nul
echo --- swarm state right after boot onto restored raft ---
docker exec %SPIKE_V5M2% docker info --format "LocalNodeState={{.Swarm.LocalNodeState}} ControlAvailable={{.Swarm.ControlAvailable}} NodeID={{.Swarm.NodeID}}" >> artifacts\logs\v5-restore-info.log
type artifacts\logs\v5-restore-info.log
ping -n 11 127.0.0.1 >nul
docker exec %SPIKE_V5M2% docker info --format "LocalNodeState={{.Swarm.LocalNodeState}} ControlAvailable={{.Swarm.ControlAvailable}} Error={{.Swarm.Error}}" >> artifacts\logs\v5-restore-info.log

echo ===== V5D.6 attempt docker swarm init --force-new-cluster =====
>artifacts\logs\v5-fnc-attempt.txt 2>&1 docker exec %SPIKE_V5M2% docker swarm init --force-new-cluster
type artifacts\logs\v5-fnc-attempt.txt
>>artifacts\logs\v5-restore-info.log echo force-new-cluster rc=%ERRORLEVEL%
docker exec %SPIKE_V5M2% docker info --format "LocalNodeState={{.Swarm.LocalNodeState}} ControlAvailable={{.Swarm.ControlAvailable}}" >> artifacts\logs\v5-restore-info.log

echo ===== V5D.7 wait worker v5w reconnects (cap 180s) =====
set /a TRY=0
:waitjoin
docker exec %SPIKE_V5M2% docker node ls --format "{{.Hostname}}={{.Status}}" 2>nul | findstr /C:"v5w=Ready" >nul && goto joined
set /a TRY+=1
if %TRY% GEQ 180 (echo v5w never rejoined & goto joined)
ping -n 2 127.0.0.1 >nul
goto waitjoin
:joined
ping -n 5 127.0.0.1 >nul
docker exec %SPIKE_V5M2% docker node ls > artifacts\logs\v5-nodels-after-restore.txt
type artifacts\logs\v5-nodels-after-restore.txt

echo ===== V5D.8 ASSERT: service definitions restored =====
docker exec %SPIKE_V5M2% docker service ls > artifacts\logs\v5-services-after-restore.txt
type artifacts\logs\v5-services-after-restore.txt
echo --- expect c5-app-worker c5-app-mgr c6-old present; c5-new ABSENT ---
docker exec %SPIKE_V5M2% docker service ls | findstr /C:"c5-new" >nul && echo V5B-C5NEW-IN-SERVICE-LS: FOUND || echo V5B-C5NEW-IN-SERVICE-LS: ABSENT

echo ===== V5D.9 ASSERT: c5-app-worker task identity continuous =====
docker exec %SPIKE_V5M2% docker service ps c5-app-worker --format "{{.ID}} {{.Name}} {{.Node}} {{.CurrentState}}" > artifacts\logs\v5-taskid-after-restore.txt
type artifacts\logs\v5-taskid-after-restore.txt
docker exec %SPIKE_V5M2% docker service ps c5-app-mgr --format "{{.ID}} {{.Name}} {{.Node}} {{.CurrentState}}"

echo ===== V5D.10 ASSERT: c6-old resurrected; volume data intact =====
docker exec %SPIKE_V5M2% docker service ps c6-old
docker exec %SPIKE_V5W% sh -c "sh /tmp/s.sh c6-old c6vol /data read"
echo --- marker: STAMP-OK with token c6-original-on-v5w = rollback resurrection kept data ---

echo ===== V5D.11 V5b: c5-new container fate on v5w (t0 sample) =====
docker exec %SPIKE_V5W% sh -c "echo t0-ms=$(/opt/probe ts); docker ps -a --format '{{.ID}} {{.Names}} {{.Status}}' | grep -i c5-new; docker ps --format '{{.Names}}' | grep -q c5-new && echo V5B-C5NEW-CONTAINER: STILL-RUNNING || echo V5B-C5NEW-CONTAINER: GONE" > artifacts\logs\v5b-sample-t0.log
docker exec %SPIKE_V5W% docker volume ls >> artifacts\logs\v5b-sample-t0.log
type artifacts\logs\v5b-sample-t0.log

echo ===== V5D.12 done; schedule t+5min sample via v5-sample.bat =====
echo ===== V5-DEATH-RESTORE done =====
