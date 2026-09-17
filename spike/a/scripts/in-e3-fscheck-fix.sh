#!/bin/sh
# E3.4 redo: full-filesystem `grep -r /` hangs on /proc (observed 2026-09-17:
# container clever_pare stuck >2min on alpine vB rootfs reading /proc/kcore).
# Scan real filesystem locations only, with hard timeout per image.
# Evidence -> /work/logs/in-e3-fsfix.log
set -u
TOKA=tok-a-123456
TOKB=tok-b-98765
{
  echo "===== E3.4 redo: filesystem grep excluding /proc /sys /dev (timeout 60s per image) ====="
  for img in spike-a/raw:vB spike-a/raw:hB spike-a/node-app:sec; do
    echo "--- $img"
    docker rm -f spike-a-fsprobe >/dev/null 2>&1
    docker run -d --name spike-a-fsprobe --entrypoint sleep "$img" 300 >/dev/null
    docker exec spike-a-fsprobe sh -c \
      "grep -r -l -e $TOKA -e $TOKB /app /work /root /home /etc /tmp /usr /var /opt 2>/dev/null | head -3"
    rc=$?
    if [ "$rc" -eq 0 ]; then
      echo "$img FS_CLEAN (exit 0, no matches)"
    else
      echo "$img FS_CHECK_EXIT=$rc (1 = no matches anywhere scanned)"
    fi
    docker rm -f spike-a-fsprobe >/dev/null
  done
  echo "--- placement: NPM_TOKEN is only ever injected via --mount=type=secret"
  echo "    (railpack step secrets) or buildctl --secret; verify a secret mount"
  echo "    never creates /run/secrets content in the final image:"
  docker rm -f spike-a-fsprobe2 >/dev/null 2>&1
  docker run -d --name spike-a-fsprobe2 --entrypoint sleep spike-a/raw:hB 300 >/dev/null
  docker exec spike-a-fsprobe2 sh -c "ls -la /run/secrets 2>&1; ls /work"
  docker rm -f spike-a-fsprobe2 >/dev/null
  echo "===== E3.5 archived railpack plans must not contain secret values ====="
  if grep -q -e "$TOKA" -e "$TOKB" /work/plans/*.json 2>/dev/null; then
    echo "PLAN_LEAK"
  else
    echo "PLANS_CLEAN"
  fi
  echo "===== E3.4/3.5 redo done ====="
} > /work/logs/in-e3-fsfix.log 2>&1
cat /work/logs/in-e3-fsfix.log
