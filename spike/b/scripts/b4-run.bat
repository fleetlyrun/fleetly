:: B4 host orchestrator: Traefik HTTP-provider delivery discipline
:: (good / empty / single-app-broken / unreachable config service).
:: Prereq: b0-up.bat done (dind running).
:: Rerun: (repo root)  spike\b\scripts\b4-run.bat   (~2 min)
call spike\b\scripts\env-common.bat
cd /d %SPIKE_B_DIR%
if not exist artifacts\logs mkdir artifacts\logs
docker inspect %SPIKE_DIND% >nul 2>&1 || (echo dind not running - run b0-up.bat first & exit /b 1)
docker exec %SPIKE_DIND% sh -c "cp /work-src/scripts/in-b4-provider.sh /tmp/b4.sh && sed -i 's/\r$//' /tmp/b4.sh && sh /tmp/b4.sh" > artifacts\logs\in-b4-provider.log 2>&1
type artifacts\logs\in-b4-provider.log
findstr /C:"B4-PROVIDER-EXPERIMENT-DONE" artifacts\logs\in-b4-provider.log >nul
if errorlevel 1 (echo B4 INCOMPLETE & exit /b 1)
echo B4 ok
