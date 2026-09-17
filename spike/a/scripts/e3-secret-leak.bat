:: E3 orchestrator (host): runs scripts/in-e3-leak.sh inside the running
:: spike-a-dind. Requires images built by e2a (railpack) and e2c (raw).
:: Rerun: (repo root)  spike\a\scripts\e3-secret-leak.bat  (needs e0-up+e2a+e2c)
call spike\a\scripts\env-common.bat
cd /d %SPIKE_A_DIR%
if not exist artifacts\logs mkdir artifacts\logs
:: re-stage scripts from the live mount (edits propagate; /work copy ages)
docker exec %SPIKE_DIND% sh -c "cp -r /work-src/scripts /work/ && find /work/scripts -type f -exec sed -i 's/\r$//' {} +"
docker exec %SPIKE_DIND% sh /work/scripts/in-e3-leak.sh > artifacts\logs\in-e3.log 2>&1
echo inner exit=%ERRORLEVEL%
type artifacts\logs\in-e3.log
docker exec %SPIKE_DIND% tar -cf - -C /work logs plans > artifacts\dind-export.tar
tar -xf artifacts\dind-export.tar -C artifacts
del artifacts\dind-export.tar
echo ===== e3 orchestrator done =====
