:: Restart a killed dind node so its swarm identity (certs in
:: /var/lib/docker/swarm) comes back on the same node. Tries plain
:: `docker start` first (clean-ish cases); if dockerd keeps failing to boot
:: (dirty-exit leaves stale containerd.pid/socket that deterministically
:: breaks the next boot), falls back to rescue-dind (filesystem-preserving
:: recreate under the same name/hostname/IP).
:: usage: noderestart.bat <container-name> <hostname> <ip>
set "TARGET=%1"
set "HNAME=%2"
set "IPADDR=%3"
docker inspect %TARGET% --format "{{.State.Status}}" | findstr /C:"running" >nul || docker start %TARGET%
set /a TRY=0
:waitd
docker exec %TARGET% docker info >nul 2>&1
if not errorlevel 1 goto up
set /a TRY+=1
if %TRY% GEQ 40 goto stuck
ping -n 3 127.0.0.1 >nul
goto waitd
:stuck
docker inspect %TARGET% --format "{{.State.Status}}" | findstr /C:"running" >nul
if errorlevel 1 (
  echo [noderestart] plain start failed - falling back to rescue
  call spike\c\scripts\rescue-dind.bat %TARGET% %HNAME% %IPADDR%
  if errorlevel 1 exit /b 1
  exit /b 0
)
echo [noderestart] container running but dockerd still not answering after 120s
exit /b 1
:up
echo [noderestart] OK %TARGET% inner dockerd ready via plain start
exit /b 0
