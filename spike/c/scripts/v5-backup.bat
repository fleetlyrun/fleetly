:: V5-BACKUP: hot tar fallback + TRUE cold backup via `docker cp` from the
:: STOPPED manager (dockerd down = matches the admin-guide cold-backup rule),
:: byte-verify key files, then bring the manager back (same identity) to
:: continue the drill. Also starts the 25-min worker-side continuity watcher.
:: Run: (repo root)  cmd /c spike\c\scripts\v5-backup.bat
call spike\c\scripts\env-common.bat
cd /d %SPIKE_C_DIR%

echo ===== V5BK.1 start worker-side continuity watcher (25 min window, 3s) =====
docker exec -d %SPIKE_V5W% sh -c "cp /work-src/scripts/in-watch.sh /tmp/w.sh && sed -i 's/\r$//' /tmp/w.sh && sh /tmp/w.sh v5w 1500 node '' '' 3"

echo ===== V5BK.2 in-manager sha256 of key swarm files (verification basis) =====
docker exec %SPIKE_V5M% sh -c "cd /var/lib/docker/swarm && sha256sum state.json certificates/swarm-node.crt certificates/swarm-node.key && ls -la" > artifacts\logs\v5-swarm-hashes-before.txt
type artifacts\logs\v5-swarm-hashes-before.txt

echo ===== V5BK.3 HOT fallback tar (manager running; quiescent cluster) =====
docker exec %SPIKE_V5M% sh -c "tar czf /work-src/artifacts/hostbak/swarm-hot.tgz -C /var/lib/docker swarm && ls -la /work-src/artifacts/hostbak/"
if errorlevel 1 (echo hot tar failed & exit /b 1)

echo ===== V5BK.4 backup stop #1: docker stop -t 30 (dockerd graceful down) =====
docker stop -t 30 %SPIKE_V5M% || exit /b 1
ping -n 3 127.0.0.1 >nul

echo ===== V5BK.5 COLD backup: docker cp from the STOPPED manager =====
rmdir /s /q artifacts\hostbak\swarm-cp 2>nul
docker cp %SPIKE_V5M%:/var/lib/docker/swarm artifacts\hostbak\swarm-cp
if errorlevel 1 (
  echo DOCKER-CP-FROM-STOPPED-FAILED - falling back to hot tgz on restore
  echo DOCKER-CP-FROM-STOPPED-FAILED > artifacts\logs\v5-cp-verdict.txt
) else (
  echo DOCKER-CP-FROM-STOPPED-OK > artifacts\logs\v5-cp-verdict.txt
)
type artifacts\logs\v5-cp-verdict.txt
dir /s /b artifacts\hostbak\swarm-cp | findstr /C:"state.json" /C:"swarm-node" 

echo ===== V5BK.6 byte-verify: host certutil vs in-manager sha256 =====
certutil -hashfile artifacts\hostbak\swarm-cp\swarm\state.json SHA256
certutil -hashfile artifacts\hostbak\swarm-cp\swarm\certificates\swarm-node.crt SHA256
certutil -hashfile artifacts\hostbak\swarm-cp\swarm\certificates\swarm-node.key SHA256

echo ===== V5BK.7 manager back up (same container = same identity) =====
docker start %SPIKE_V5M% || exit /b 1
set /a TRY=0
:waitm
docker exec %SPIKE_V5M% docker info >nul 2>&1
if not errorlevel 1 goto mup
set /a TRY+=1
if %TRY% GEQ 60 (echo v5m dockerd not back & exit /b 1)
ping -n 2 127.0.0.1 >nul
goto waitm
:mup
ping -n 3 127.0.0.1 >nul
docker exec %SPIKE_V5M% docker node ls
docker exec %SPIKE_V5M% docker service ls
echo ===== V5-BACKUP done =====
