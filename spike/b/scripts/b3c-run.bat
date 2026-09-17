:: B3c host orchestrator: V4 round 2 - in-flight request kill (the real
:: Dokploy #5281 severity) x 3 configurations (abrupt / +serversTransport /
:: +graceful drain). Rebuilds fixtures with the upgraded probe binary first.
:: Prereq: b0-up.bat done (dind running).
:: Rerun: (repo root)  spike\b\scripts\b3c-run.bat   (~5 min)
call spike\b\scripts\env-common.bat
cd /d %SPIKE_B_DIR%
if not exist artifacts\logs mkdir artifacts\logs
docker inspect %SPIKE_DIND% >nul 2>&1 || (echo dind not running - run b0-up.bat first & exit /b 1)
docker exec %SPIKE_DIND% sh -c "cp /work-src/scripts/in-b3c-v4v2.sh /tmp/b3c.sh && sed -i 's/\r$//' /tmp/b3c.sh && sh /tmp/b3c.sh" > artifacts\logs\in-b3c-v4v2.log 2>&1
type artifacts\logs\in-b3c-v4v2.log
findstr /C:"B3C-V4V2-EXPERIMENT-DONE" artifacts\logs\in-b3c-v4v2.log >nul
if errorlevel 1 (echo B3C INCOMPLETE & exit /b 1)
echo B3C ok
