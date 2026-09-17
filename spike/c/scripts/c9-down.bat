:: C9-DOWN: full teardown of every spike-c-* resource + cleanliness check.
:: Run: (repo root)  cmd /c spike\c\scripts\c9-down.bat
call spike\c\scripts\env-common.bat
cd /d %SPIKE_C_DIR%

echo ===== teardown all spike-c dinds =====
docker rm -f %SPIKE_M0% %SPIKE_MGR% %SPIKE_W1% %SPIKE_W2% %SPIKE_V5M% %SPIKE_V5W% %SPIKE_V5M2% 2>nul

echo ===== remove host bridge network =====
docker network rm %SPIKE_NET% 2>nul

echo ===== remove leftover rescue images (rescue-dind commits) =====
for /f %%i in ('docker images --format "{{.Repository}}" ^| findstr /B /C:"spike-c-"') do docker rmi -f %%i

echo ===== prune dangling anonymous volumes (dind state volumes) =====
docker volume prune -f 2>&1 | findstr /C:"Total reclaimed"

echo ===== cleanliness check (expect NO output in each block) =====
echo --- containers named spike-c-* ---
docker ps -a --format "{{.Names}}" | findstr /B /C:"spike-c-"
echo --- networks named spike-c-* ---
docker network ls --format "{{.Name}}" | findstr /B /C:"spike-c-"
echo --- host volumes named spike-c-* ---
docker volume ls --format "{{.Name}}" | findstr /B /C:"spike-c-"
echo --- images named spike-c-* ---
docker images --format "{{.Repository}}:{{.Tag}}" | findstr /B /C:"spike-c-"
echo ===== cleanup verification done (all four blocks above must be empty) =====
