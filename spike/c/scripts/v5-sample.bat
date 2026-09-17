:: V5-SAMPLE: V5b observation sample (call with arg t5 / t30). Appends the
:: orphan question evidence to artifacts\logs\v5b-sample-<arg>.log.
:: usage: v5-sample.bat t5   (and later t30 if scheduled)
call spike\c\scripts\env-common.bat
cd /d %SPIKE_C_DIR%
set "TAG=%1"
if "%TAG%"=="" set "TAG=t5"

echo ===== V5B sample %TAG% =====
docker exec %SPIKE_V5W% sh -c "echo %TAG%-ms=$(/opt/probe ts); docker ps -a --format '{{.ID}} {{.Names}} {{.Status}}' | grep -i c5-new; docker ps --format '{{.Names}}' | grep -q c5-new && echo V5B-C5NEW-CONTAINER: STILL-RUNNING || echo V5B-C5NEW-CONTAINER: GONE; docker ps --format '{{.ID}} {{.Names}} {{.Status}}'" > artifacts\logs\v5b-sample-%TAG%.log
type artifacts\logs\v5b-sample-%TAG%.log
docker exec %SPIKE_V5M2% sh -c "echo -- service ls from restored manager --; docker service ls; echo -- c5-new service def? --; docker service ps c5-new 2>&1 | head -3; echo -- c5-app-worker task --; docker service ps c5-app-worker --format '{{.ID}} {{.Name}} {{.Node}} {{.CurrentState}}'" >> artifacts\logs\v5b-sample-%TAG%.log
type artifacts\logs\v5b-sample-%TAG%.log
echo ===== V5B sample %TAG% done =====
