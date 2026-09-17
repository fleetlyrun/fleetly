:: Spike A infrastructure bootstrap (host side).
:: Boots ONE privileged docker:29.8.1-dind with the spike module directory
:: bind-mounted at /work-src, copies the linux driver + fixtures + scripts
:: into /work inside dind, then starts the pinned buildkitd + registry
:: containers INSIDE dind. Idempotent. Run before e1..e4 orchestrators.
:: NOT used by e5/e4-rootless (own dind, same mount trick).
::
:: Transport notes (measured 2026-09-17, see FINDINGS):
::   - docker cp host -> privileged dind silently drops files (e2e/README.md).
::   - `docker exec -i ... < file` inside a .bat delivers 0 bytes (batch-
::     specific; direct shell invocation works).
::   - `type file | docker exec -i` LOSES BYTES at MB scale (driver b64 came
::     up 3,696 bytes short) and mangles binary payloads.
::   - bind mount (-v) is byte-faithful (sha256 verified) and needs no
::     transfer step: the mount is performed by the HOST dockerd.
::   - `timeout /t` fails under redirected stdin; use `ping -n` sleeps.
call spike\a\scripts\env-common.bat
cd /d %SPIKE_A_DIR%

echo ===== 0. cross-compile linux driver =====
set "GOOS=linux"
set "GOARCH=amd64"
set "CGO_ENABLED=0"
go build -o artifacts\spikerailpack-linux .\cmd\spikerailpack || exit /b 1
set "GOOS="
set "GOARCH="
set "CGO_ENABLED="

echo ===== 1. boot privileged dind (module dir mounted at /work-src) =====
docker rm -f %SPIKE_DIND% 2>nul
docker pull %SPIKE_DIND_IMAGE% 1>nul 2>&1
docker run -d --name %SPIKE_DIND% --privileged -v "%SPIKE_A_DIR%:/work-src" %SPIKE_DIND_IMAGE% || exit /b 1

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

echo ===== 2. stage /work inside dind from the mount (sha256 verified) =====
docker exec %SPIKE_DIND% sh -c "rm -rf /work && mkdir -p /work/logs /work/plans /work/artifacts && cp -r /work-src/fixtures /work-src/scripts /work/ && cp /work-src/artifacts/spikerailpack-linux /work/spikerailpack && chmod +x /work/spikerailpack && sed -i 's/\r$//' /work/fixtures/node-app/.npmrc /work/scripts/buildkitd.toml && find /work/scripts -type f -name '*.sh' -exec sed -i 's/\r$//' {} + && find /work/scripts/raw-ctx -type f -exec sed -i 's/\r$//' {} + && sha256sum /work/spikerailpack" || exit /b 1

echo ===== 3. driver self-check =====
docker exec %SPIKE_DIND% /work/spikerailpack --version || exit /b 1

echo ===== 4. inner infra (buildkitd + registry inside dind) =====
docker exec %SPIKE_DIND% sh /work/scripts/in-e0-infra.sh || exit /b 1
echo ===== e0-up done =====
