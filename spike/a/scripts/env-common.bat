:: Spike A shared environment for orchestrator scripts.
:: Host .bat files are thin orchestrators (boot dind, stream files in via
:: exec+stdin, run inner sh scripts, fetch logs); all experiment logic runs
:: INSIDE docker:29.8.1-dind because railpack plan generation requires mise
:: (Linux-only) - verified: on the Windows host prepare fails with
:: "Failed to ensure mise is installed: ... binary not found in archive".
set "SPIKE_A_DIR=%~dp0.."
set "GOWORK=off"
set "GOPROXY=goproxy.cn"
set "SPIKE_BK_IMAGE=moby/buildkit:v0.32.2"
set "SPIKE_REGISTRY_IMAGE=registry:2.8.3"
set "SPIKE_DIND_IMAGE=docker:29.8.1-dind"
set "SPIKE_DIND=spike-a-dind"
