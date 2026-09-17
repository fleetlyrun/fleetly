:: B0 host orchestrator: cross-compile the static probe binary, boot a
:: privileged docker:29.8.1-dind (spike/b dir bind-mounted at /work-src),
:: then run the inner infra bootstrap (swarm init, overlay, alpine+traefik
:: pulls, fixture builds, cfgsvc + traefik up, sanity checks).
:: Rerun: (repo root)  spike\b\scripts\b0-up.bat   (~3-6 min, traefik pull dominates)
call spike\b\scripts\env-common.bat
cd /d %SPIKE_B_DIR%
if not exist artifacts\logs mkdir artifacts\logs

echo ===== B0.1 cross-compile probe (linux/amd64, CGO off = static) =====
set "GOOS=linux"
set "GOARCH=amd64"
set "CGO_ENABLED=0"
go build -trimpath -ldflags "-s -w" -o dockerctx\probe .\cmd\probe || exit /b 1
set "GOOS="
set "GOARCH="
set "CGO_ENABLED="
dir dockerctx\probe | find /C "probe" >nul

echo ===== B0.2 boot privileged dind (bind mount = byte-faithful transport) =====
docker rm -f %SPIKE_DIND% 2>nul
docker pull %SPIKE_DIND_IMAGE% 1>nul 2>&1
docker run -d --name %SPIKE_DIND% --privileged -v "%SPIKE_B_DIR%:/work-src" %SPIKE_DIND_IMAGE% || exit /b 1

echo ===== B0.3 wait for inner dockerd (60s deadline) =====
set /a TRY=0
:waitloop
docker exec %SPIKE_DIND% docker info >nul 2>&1
if %ERRORLEVEL%==0 goto ready
set /a TRY+=1
if %TRY% GEQ 60 (echo inner dockerd not ready & exit /b 1)
ping -n 2 127.0.0.1 >nul
goto waitloop
:ready
ping -n 3 127.0.0.1 >nul
docker exec %SPIKE_DIND% docker version --format "inner engine: {{.Server.Version}}"

echo ===== B0.4 stage + run inner infra bootstrap =====
docker exec %SPIKE_DIND% sh -c "cp /work-src/scripts/in-b0-infra.sh /tmp/b0.sh && sed -i 's/\r$//' /tmp/b0.sh && sh /tmp/b0.sh" > artifacts\logs\in-b0-infra.log 2>&1
type artifacts\logs\in-b0-infra.log
findstr /C:"B0-INFRA-OK" artifacts\logs\in-b0-infra.log >nul
if errorlevel 1 (echo B0 INFRA FAILED & exit /b 1)
echo ===== B0 done =====
