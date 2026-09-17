:: B3 host orchestrator: V3 (routing stability) + V4 (stale keep-alive +
:: serversTransport mitigation) through Traefik v3 (HTTP provider).
:: Prereq: b0-up.bat done (dind running).
:: Rerun: (repo root)  spike\b\scripts\b3-run.bat   (~4-5 min)
call spike\b\scripts\env-common.bat
cd /d %SPIKE_B_DIR%
if not exist artifacts\logs mkdir artifacts\logs
docker inspect %SPIKE_DIND% >nul 2>&1 || (echo dind not running - run b0-up.bat first & exit /b 1)
docker exec %SPIKE_DIND% sh -c "cp /work-src/scripts/in-b3-v3v4.sh /tmp/b3.sh && sed -i 's/\r$//' /tmp/b3.sh && sh /tmp/b3.sh" > artifacts\logs\in-b3-v3v4.log 2>&1
type artifacts\logs\in-b3-v3v4.log
findstr /C:"B3-V3V4-EXPERIMENT-DONE" artifacts\logs\in-b3-v3v4.log >nul
if errorlevel 1 (echo B3 INCOMPLETE & exit /b 1)
echo B3 ok
