:: Spike A cleanup: removes the spike-a-dind (which owns all inner spike-a-*
:: containers/images) plus any stray spike-a-* resources on the host.
:: Rerun: (repo root)  spike\a\scripts\e0-down.bat
echo ===== removing dind (owns inner buildkitd/registry/swarm leftovers) =====
docker rm -f spike-a-dind 2>nul
echo ===== stray spike-a-* containers on host =====
for /f "tokens=*" %%c in ('docker ps -a --format "{{.Names}}" ^| findstr /R "^spike-a-"') do docker rm -f %%c
echo ===== stray spike-a/* images on host =====
for /f "tokens=*" %%i in ('docker images --format "{{.Repository}}:{{.Tag}}" ^| findstr /R "^spike-a/"') do docker rmi -f %%i
echo ===== pinned tool images pulled by spike =====
docker rmi -f moby/buildkit:v0.32.2 moby/buildkit:v0.32.2-rootless registry:2.8.3 2>nul
echo ===== volumes =====
for /f "tokens=*" %%v in ('docker volume ls --format "{{.Name}}" ^| findstr /R "spike-a"') do docker volume rm %%v
echo ===== networks =====
docker network rm spike-a-net 2>nul
echo ===== residual check (nothing between the markers = clean) =====
echo ---BEGIN-RESIDUAL---
docker ps -a --format "{{.Names}}" | findstr /R "^spike-a-"
docker images --format "{{.Repository}}" | findstr /R "^spike-a/"
docker volume ls --format "{{.Name}}" | findstr /R "spike-a"
echo ---END-RESIDUAL---
echo ===== e0-down done =====
