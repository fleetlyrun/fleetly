#!/bin/sh
# 语法检查全部 e2e 脚本（临时辅助）。
cd /d/Codes/qiulin/fleetly || exit 1
rc=0
for f in cron databases logs-victorialogs s3-rustfs terminal metrics notifications multinode-rehearsal control-plane-tls auth smoke; do
    if sh -n "e2e/$f.sh"; then
        echo "OK-$f"
    else
        echo "SYNTAXFAIL-$f"
        rc=1
    fi
done
exit $rc
