:: E5 host-side orchestrator: boots a privileged docker:29.8.1-dind (module
:: dir mounted at /work-src), runs the inner V2 verification script inside,
:: then greps the dockerd log (container stdout) for pull attempts.
:: Rerun: (repo root)  spike\a\scripts\e5-run.bat   (takes ~5 minutes)
:: Transport rationale: docker cp host->privileged dind silently drops files
:: (e2e/README.md); exec+stdin from a .bat delivers 0 bytes; type|pipe loses
:: bytes at MB scale. Bind mount is byte-faithful and needs no transfer.
:: `timeout /t` is unusable under redirected stdin; use `ping -n` sleeps.
call spike\a\scripts\env-common.bat
cd /d %SPIKE_A_DIR%
if not exist artifacts\logs mkdir artifacts\logs

echo ===== E5.0 boot privileged dind =====
docker rm -f spike-a-dind 2>nul
docker pull %SPIKE_DIND_IMAGE% 1>nul 2>&1
docker run -d --name spike-a-dind --privileged -v "%SPIKE_A_DIR%:/work-src" %SPIKE_DIND_IMAGE% || exit /b 1

echo ===== E5.1 wait for inner dockerd (60s deadline) =====
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
docker exec spike-a-dind docker version --format "inner engine: {{.Server.Version}}"

echo ===== E5.2 stage + de-CRLF inner script, then run =====
docker exec spike-a-dind sh -c "cp /work-src/scripts/e5-v2-digest.sh /tmp/e5.sh && sed -i 's/\r$//' /tmp/e5.sh && chmod +x /tmp/e5.sh && head -2 /tmp/e5.sh" || exit /b 1
docker exec spike-a-dind sh /tmp/e5.sh
set "INNER_EXIT=%ERRORLEVEL%"
echo inner exit=%INNER_EXIT%

echo ===== E5.3 inner report =====
docker exec spike-a-dind cat /tmp/e5-report.txt > artifacts\logs\e5-report.log
type artifacts\logs\e5-report.log

echo ===== E5.4 dockerd log: pull-attempt grep over the whole run =====
docker logs spike-a-dind > artifacts\logs\e5-dockerd-full.log 2>&1
findstr /I /C:"pull" artifacts\logs\e5-dockerd-full.log
echo --- pull-line count:
find /C /I "pull" artifacts\logs\e5-dockerd-full.log
echo --- resolver/digest lines (context):
findstr /I /C:"digest" /C:"manifest unknown" /C:"no such image" artifacts\logs\e5-dockerd-full.log

echo ===== E5.5 dind teardown =====
docker rm -f spike-a-dind
echo ===== E5 done (inner exit=%INNER_EXIT%) =====
