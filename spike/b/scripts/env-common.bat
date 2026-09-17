:: Spike B shared environment for orchestrator scripts.
:: Host .bat files are thin orchestrators (boot dind, stage inner scripts via
:: bind mount, run them, fetch logs); all experiment logic runs INSIDE
:: docker:29.8.1-dind because swarm/routing behavior must match the CI gate
:: engine. Bind mount is the only byte-faithful transport (spike/a lesson 5).
set "SPIKE_B_DIR=%~dp0.."
set "SPIKE_DIND_IMAGE=docker:29.8.1-dind"
set "SPIKE_DIND=spike-b-dind"
set "GOWORK=off"
