:: B6 host orchestrator: snapshot-replay rollback timing (<60s acceptance).
:: Prereq: b0-up.bat done (dind running).
:: Rerun: (repo root)  spike\b\scripts\b6-run.bat   (~2 min)
call spike\b\scripts\env-common.bat
cd /d %SPIKE_B_DIR%
if not exist artifacts\logs mkdir artifacts\logs
docker inspect %SPIKE_DIND% >nul 2>&1 || (echo dind not running - run b0-up.bat first & exit /b 1)
docker exec %SPIKE_DIND% sh -c "cp /work-src/scripts/in-b6-rr.sh /tmp/b6.sh && sed -i 's/\r$//' /tmp/b6.sh && sh /tmp/b6.sh" > artifacts\logs\in-b6-rr.log 2>&1
type artifacts\logs\in-b6-rr.log
findstr /C:"B6-RR-EXPERIMENT-DONE" artifacts\logs\in-b6-rr.log >nul
if errorlevel 1 (echo B6 INCOMPLETE & exit /b 1)
echo B6 ok
