:: B9 host orchestrator: full teardown of the Spike B environment.
:: The dind container holds ALL experiment state (swarm, overlay, fixtures,
:: traefik, probers) - removing it is the complete cleanup. This script also
:: verifies nothing spike-b-prefixed leaked to the host docker.
:: Rerun: (repo root)  spike\b\scripts\b9-down.bat
call spike\b\scripts\env-common.bat

echo ===== B9.1 remove dind (destroys all inner state) =====
docker rm -f %SPIKE_DIND% 2>nul

echo ===== B9.2 verify no spike-b leftovers on host =====
docker ps -a --format "{{.Names}}" | find "spike-b"
if errorlevel 1 echo OK no spike-b containers on host
docker images --format "{{.Repository}}:{{.Tag}}" | find "spike-b/"
if errorlevel 1 echo OK no spike-b images on host
docker network ls --format "{{.Name}}" | find "spike-b"
if errorlevel 1 echo OK no spike-b networks on host
echo ===== B9 done =====
