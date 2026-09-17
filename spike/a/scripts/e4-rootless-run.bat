:: E4 host-side orchestrator for the rootless probe inside 29.8.1 dind.
:: Rerun: (repo root)  spike\a\scripts\e4-rootless-run.bat
:: Transport: bind mount (exec+stdin from a .bat delivers 0 bytes).
call spike\a\scripts\env-common.bat
cd /d %SPIKE_A_DIR%
if not exist artifacts\logs mkdir artifacts\logs

docker rm -f spike-a-dind 2>nul
docker run -d --name spike-a-dind --privileged -v "%SPIKE_A_DIR%:/work-src" %SPIKE_DIND_IMAGE% || exit /b 1
set /a TRY=0
:waitloop
docker exec spike-a-dind docker info >nul 2>&1
if %ERRORLEVEL%==0 goto ready
set /a TRY+=1
if %TRY% GEQ 60 (echo inner dockerd not ready & exit /b 1)
ping -n 2 127.0.0.1 >nul
goto waitloop
:ready
ping -n 3 127.0.0.1 >nul
docker exec spike-a-dind sh -c "cp /work-src/scripts/e4-rootless.sh /tmp/e4rl.sh && sed -i 's/\r$//' /tmp/e4rl.sh && sh /tmp/e4rl.sh"
echo inner exit=%ERRORLEVEL%
echo ===== E4-rootless report =====
docker exec spike-a-dind cat /tmp/e4-rootless-report.txt
docker exec spike-a-dind cat /tmp/e4-rootless-report.txt > artifacts\logs\e4-rootless-report.log
docker rm -f spike-a-dind
echo ===== e4-rootless-run done =====
