:: Spike C shared environment for host orchestrator scripts.
:: All swarm behavior experiments run INSIDE docker:29.8.1-dind containers
:: (multi-node topology = several dind containers on one host bridge network).
:: Bind mount is the only byte-faithful transport (spike/a lesson 5).
:: Static IPs on spike-c-br so the V5 restored manager can reclaim the exact
:: advertise address of the dead manager and workers reconnect untouched.
set "SPIKE_C_DIR=%~dp0.."
set "SPIKE_DIND_IMAGE=docker:29.8.1-dind"
set "SPIKE_NET=spike-c-br"
set "SPIKE_SUBNET=10.10.0.0/24"
set "SPIKE_GW=10.10.0.1"
set "SPIKE_M0=spike-c-m0"
set "SPIKE_MGR=spike-c-mgr"
set "SPIKE_W1=spike-c-w1"
set "SPIKE_W2=spike-c-w2"
set "SPIKE_V5M=spike-c-v5m"
set "SPIKE_V5W=spike-c-v5w"
set "SPIKE_V5M2=spike-c-v5m2"
set "SPIKE_IP_MGR=10.10.0.10"
set "SPIKE_IP_W1=10.10.0.11"
set "SPIKE_IP_W2=10.10.0.12"
set "SPIKE_IP_V5M=10.10.0.20"
set "SPIKE_IP_V5W=10.10.0.21"
set "GOWORK=off"
