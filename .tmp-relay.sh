#!/bin/sh
# Relay engine images via node2 (host outbound is down): pull+save on node2,
# local load. Args: image list.
set -eu
for img in "$@"; do
  name=$(echo "$img" | tr ':/' '__')
  docker pull "$img" || true
  docker save "$img" -o "/tmp/$name.tar"
done
ls -la /tmp/*.tar
