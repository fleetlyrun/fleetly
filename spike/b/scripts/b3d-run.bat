:: B3d host orchestrator: V4 round 3 - POST (non-replayable) requests vs the
:: in-flight task kill (abrupt vs graceful-drain backend).
:: Prereq: b0-up.bat done (dind running).
:: Rerun: (repo root)  spike\b\scripts\b3d-run.bat   (~4 min)
call spike\b\scripts\env-common.bat
cd /d %SPIKE_B_DIR%
if not exist artifacts\logs mkdir artifacts\logs
docker inspect %SPIKE_DIND% >nul 2>&1 || (echo dind not running - run b0-up.bat first & exit /b 1)
docker exec %SPIKE_DIND% sh -c "cp /work-src/scripts/in-b3d-v4post.sh /tmp/b3d.sh && sed -i 's/\r$//' /tmp/b3d.sh && sh /tmp/b3d.sh" > artifacts\logs\in-b3d-v4post.log 2>&1
type artifacts\logs\in-b3d-v4post.log
findstr /C:"B3D-V4POST-EXPERIMENT-DONE" artifacts\logs\in-b3d-v4post.log >nul
if errorlevel 1 (echo B3D INCOMPLETE & exit /b 1)
echo B3D ok
