:: E2a+E2c(railpack) orchestrator (host): runs scripts/in-e2a-build-cache-local.sh
:: inside the running spike-a-dind.
:: Rerun: (repo root)  spike\a\scripts\e2a-build-cache-local.bat  (needs e0-up.bat)
call spike\a\scripts\env-common.bat
cd /d %SPIKE_A_DIR%
if not exist artifacts\logs mkdir artifacts\logs
:: re-stage scripts from the live mount (edits propagate; /work copy ages)
docker exec %SPIKE_DIND% sh -c "cp -r /work-src/scripts /work/ && find /work/scripts -type f -exec sed -i 's/\r$//' {} +"
docker exec %SPIKE_DIND% sh /work/scripts/in-e2a-build-cache-local.sh > artifacts\logs\in-e2a.log 2>&1
echo inner exit=%ERRORLEVEL%
type artifacts\logs\in-e2a.log
docker exec %SPIKE_DIND% tar -cf - -C /work logs plans > artifacts\dind-export.tar
tar -xf artifacts\dind-export.tar -C artifacts
del artifacts\dind-export.tar
echo ===== e2a orchestrator done =====
