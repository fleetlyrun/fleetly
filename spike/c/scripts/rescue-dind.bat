:: Rescue-restart an exited dind node whose dockerd fails with "timeout
:: waiting for containerd to start" (stale containerd.pid/socket survives the
:: SIGKILL in the container FS and breaks every plain `docker start`).
:: Approach: commit the stopped container's filesystem (swarm certs, volumes,
:: images all preserved = node identity preserved), recreate the container
:: under the SAME name/hostname/IP from the committed image with an entrypoint
:: that clears the stale runtime files, then exec the standard dind entrypoint.
:: usage: rescue-dind.bat <container-name> <hostname> <ip>
call spike\c\scripts\env-common.bat
set "TARGET=%1"
set "HNAME=%2"
set "IPADDR=%3"
set "IMG=%TARGET%-rescued"

echo [rescue-dind] commit %TARGET% filesystem -^> %IMG%
docker commit %TARGET% %IMG% || exit /b 1
docker rm %TARGET% || exit /b 1
echo [rescue-dind] recreate %TARGET% (same name/hostname/ip, cleaned runtime state)
docker run -d --name %TARGET% --hostname %HNAME% --privileged --network %SPIKE_NET% --ip %IPADDR% -v "%SPIKE_C_DIR%:/work-src" --entrypoint sh %IMG% -c "rm -f /run/docker/containerd/containerd.pid /run/docker/containerd/containerd.sock /var/run/docker.pid /var/run/docker.sock; exec /usr/local/bin/dockerd-entrypoint.sh" || exit /b 1
set /a TRY=0
:waitd
docker exec %TARGET% docker info >nul 2>&1
if not errorlevel 1 goto up
set /a TRY+=1
if %TRY% GEQ 60 goto fail
ping -n 3 127.0.0.1 >nul
goto waitd
:up
echo [rescue-dind] OK %TARGET% inner dockerd ready
exit /b 0
:fail
echo [rescue-dind] FAILED
exit /b 1
