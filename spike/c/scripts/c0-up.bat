:: C0 host orchestrator: cross-compile probe, boot dind spike-c-m0, run C1
:: (swarm init transparency). Rerun: (repo root)  spike\c\scripts\c0-up.bat
call spike\c\scripts\env-common.bat
cd /d %SPIKE_C_DIR%
if not exist artifacts\logs mkdir artifacts\logs
if not exist artifacts\hostbak mkdir artifacts\hostbak
if not exist dockerctx mkdir dockerctx

echo ===== C0.1 cross-compile probe (linux/amd64 static, stdlib only) =====
set "GOOS=linux"
set "GOARCH=amd64"
set "CGO_ENABLED=0"
go build -trimpath -ldflags "-s -w" -o dockerctx\probe .\cmd\probe || exit /b 1
set "GOOS="
set "GOARCH="
set "CGO_ENABLED="
if not exist dockerctx\probe (echo probe build missing & exit /b 1)

echo ===== C0.2 host bridge network spike-c-br =====
docker network ls --format "{{.Name}}" | findstr /x "%SPIKE_NET%" >nul || docker network create --driver bridge --subnet %SPIKE_SUBNET% --gateway %SPIKE_GW% %SPIKE_NET% || exit /b 1

echo ===== C0.3 boot dind %SPIKE_M0% (bind mount /work-src = byte-faithful) =====
docker rm -f %SPIKE_M0% 2>nul
docker run -d --name %SPIKE_M0% --hostname m0 --privileged --network %SPIKE_NET% -v "%SPIKE_C_DIR%:/work-src" %SPIKE_DIND_IMAGE% || exit /b 1

echo ===== C0.4 wait inner dockerd (60s deadline) =====
set /a TRY=0
:waitloop
docker exec %SPIKE_M0% docker info >nul 2>&1
if not errorlevel 1 goto ready
set /a TRY+=1
if %TRY% GEQ 60 (echo inner dockerd not ready & exit /b 1)
ping -n 2 127.0.0.1 >nul
goto waitloop
:ready
ping -n 2 127.0.0.1 >nul

echo ===== C0.5 stage probe + alpine, run C1 =====
docker exec %SPIKE_M0% sh -c "cp /work-src/dockerctx/probe /opt/probe && chmod +x /opt/probe" || exit /b 1
docker exec %SPIKE_M0% docker pull alpine:3.20 1>nul || exit /b 1
docker exec %SPIKE_M0% sh -c "cp /work-src/scripts/in-c1-init.sh /tmp/c1.sh && sed -i 's/\r$//' /tmp/c1.sh && sh /tmp/c1.sh" > artifacts\logs\in-c1.log 2>&1
type artifacts\logs\in-c1.log
findstr /C:"C1-OK" artifacts\logs\in-c1.log >nul
if errorlevel 1 (echo C1 FAILED & exit /b 1)
echo ===== C0 done =====
