:: B5 host orchestrator: failure matrix (crash-on-start / health-never-pass
:: / pull-failure) x (start-first, stop-first), split into two inner runs to
:: respect the 5-minute chunk rule.
:: Prereq: b0-up.bat done (dind running).
:: Rerun: (repo root)  spike\b\scripts\b5-run.bat   (~5-7 min total)
call spike\b\scripts\env-common.bat
cd /d %SPIKE_B_DIR%
if not exist artifacts\logs mkdir artifacts\logs
docker inspect %SPIKE_DIND% >nul 2>&1 || (echo dind not running - run b0-up.bat first & exit /b 1)

docker exec %SPIKE_DIND% sh -c "cp /work-src/scripts/in-b5a-matrix.sh /tmp/b5a.sh && sed -i 's/\r$//' /tmp/b5a.sh && sh /tmp/b5a.sh" > artifacts\logs\in-b5a-matrix.log 2>&1
type artifacts\logs\in-b5a-matrix.log
findstr /C:"B5A-MATRIX-DONE" artifacts\logs\in-b5a-matrix.log >nul
if errorlevel 1 (echo B5A INCOMPLETE & exit /b 1)

docker exec %SPIKE_DIND% sh -c "cp /work-src/scripts/in-b5b-matrix.sh /tmp/b5b.sh && sed -i 's/\r$//' /tmp/b5b.sh && sh /tmp/b5b.sh" > artifacts\logs\in-b5b-matrix.log 2>&1
type artifacts\logs\in-b5b-matrix.log
findstr /C:"B5B-MATRIX-DONE" artifacts\logs\in-b5b-matrix.log >nul
if errorlevel 1 (echo B5B INCOMPLETE & exit /b 1)
echo B5 ok
