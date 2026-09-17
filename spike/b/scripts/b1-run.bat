:: B1 host orchestrator: V1 (health fail -> no traffic switch) + B2
:: (zero-cost restore, task id comparison) + B2b field-dirty matrix.
:: Prereq: b0-up.bat done (dind running).
:: Rerun: (repo root)  spike\b\scripts\b1-run.bat   (~2-3 min)
call spike\b\scripts\env-common.bat
cd /d %SPIKE_B_DIR%
if not exist artifacts\logs mkdir artifacts\logs
docker inspect %SPIKE_DIND% >nul 2>&1 || (echo dind not running - run b0-up.bat first & exit /b 1)
docker exec %SPIKE_DIND% sh -c "cp /work-src/scripts/in-b1-v1b2.sh /tmp/b1.sh && sed -i 's/\r$//' /tmp/b1.sh && sh /tmp/b1.sh" > artifacts\logs\in-b1-v1b2.log 2>&1
type artifacts\logs\in-b1-v1b2.log
findstr /C:"B1-EXPERIMENT-DONE" artifacts\logs\in-b1-v1b2.log >nul
if errorlevel 1 (echo B1 INCOMPLETE & exit /b 1)
echo B1 ok
