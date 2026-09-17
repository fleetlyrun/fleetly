:: B2 host orchestrator: B3 LB endpoint timing (B3a healthy slow-start /
:: B3b never-healthy / B3c no-healthcheck) - the TOP PRIORITY experiment.
:: Prereq: b0-up.bat done (dind running).
:: Rerun: (repo root)  spike\b\scripts\b2-run.bat   (~4-5 min)
call spike\b\scripts\env-common.bat
cd /d %SPIKE_B_DIR%
if not exist artifacts\logs mkdir artifacts\logs
docker inspect %SPIKE_DIND% >nul 2>&1 || (echo dind not running - run b0-up.bat first & exit /b 1)
docker exec %SPIKE_DIND% sh -c "cp /work-src/scripts/in-b2-b3.sh /tmp/b2.sh && sed -i 's/\r$//' /tmp/b2.sh && sh /tmp/b2.sh" > artifacts\logs\in-b2-b3.log 2>&1
type artifacts\logs\in-b2-b3.log
findstr /C:"B2-B3-EXPERIMENT-DONE" artifacts\logs\in-b2-b3.log >nul
if errorlevel 1 (echo B2 INCOMPLETE & exit /b 1)
echo B2 ok
