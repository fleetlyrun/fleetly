:: C-PREP: replace the single C1 dind with the two-node swarm used by
:: C2 (reschedule), C3 (V6a) and C4 (V6b): mgr (10.10.0.10) + w1 (10.10.0.11)
:: on host bridge spike-c-br, worker joined via token, platform identity
:: labels fleetly.node-id set on both nodes.
:: Rerun: (repo root)  spike\c\scripts\c2-prep.bat
call spike\c\scripts\env-common.bat
cd /d %SPIKE_C_DIR%

echo ===== PREP.1 teardown leftovers =====
docker rm -f %SPIKE_M0% %SPIKE_MGR% %SPIKE_W1% 2>nul

echo ===== PREP.2 boot mgr + w1 (static IPs, hostnames mgr/w1) =====
docker run -d --name %SPIKE_MGR% --hostname mgr --privileged --network %SPIKE_NET% --ip %SPIKE_IP_MGR% -v "%SPIKE_C_DIR%:/work-src" %SPIKE_DIND_IMAGE% || exit /b 1
docker run -d --name %SPIKE_W1% --hostname w1 --privileged --network %SPIKE_NET% --ip %SPIKE_IP_W1% -v "%SPIKE_C_DIR%:/work-src" %SPIKE_DIND_IMAGE% || exit /b 1

echo ===== PREP.3 wait inner dockerd both =====
set /a TRY=0
:waitmgr
docker exec %SPIKE_MGR% docker info >nul 2>&1
if not errorlevel 1 goto mgrready
set /a TRY+=1
if %TRY% GEQ 60 (echo mgr dockerd not ready & exit /b 1)
ping -n 2 127.0.0.1 >nul
goto waitmgr
:mgrready
set /a TRY=0
:waitw1
docker exec %SPIKE_W1% docker info >nul 2>&1
if not errorlevel 1 goto w1ready
set /a TRY+=1
if %TRY% GEQ 60 (echo w1 dockerd not ready & exit /b 1)
ping -n 2 127.0.0.1 >nul
goto waitw1
:w1ready
ping -n 2 127.0.0.1 >nul

echo ===== PREP.4 stage probe binary at /opt/probe on both nodes =====
docker exec %SPIKE_MGR% sh -c "cp /work-src/dockerctx/probe /opt/probe && chmod +x /opt/probe" || exit /b 1
docker exec %SPIKE_W1% sh -c "cp /work-src/dockerctx/probe /opt/probe && chmod +x /opt/probe" || exit /b 1
echo ===== PREP.5 pre-pull alpine:3.20 on both nodes =====
docker exec %SPIKE_MGR% docker pull alpine:3.20 1>nul 2>&1
docker exec %SPIKE_W1% docker pull alpine:3.20 1>nul 2>&1

echo ===== PREP.6 swarm init + join =====
docker exec %SPIKE_MGR% docker swarm init --advertise-addr eth0 || exit /b 1
for /f %%t in ('docker exec %SPIKE_MGR% docker swarm join-token -q worker') do set "JTOK=%%t"
docker exec %SPIKE_W1% docker swarm join %SPIKE_IP_MGR%:2377 --token %JTOK% || exit /b 1

echo ===== PREP.7 platform identity labels (fleetly.node-id) =====
docker exec %SPIKE_MGR% docker node update --label-add fleetly.node-id=mgr mgr || exit /b 1
docker exec %SPIKE_MGR% docker node update --label-add fleetly.node-id=w1 w1 || exit /b 1

echo ===== PREP.8 sanity =====
docker exec %SPIKE_MGR% docker node ls || exit /b 1
docker exec %SPIKE_MGR% docker node ls --format "{{.Hostname}}={{.Status}}" | findstr /C:"mgr=Ready" /C:"w1=Ready" >nul || (echo cluster not ready & exit /b 1)
echo ===== C-PREP done =====
