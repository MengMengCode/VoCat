#!/usr/bin/env bash
set -euo pipefail

# Buildx can use a local BuildKit image if its own pull encounters a transient
# registry failure. Populate that image before setup-buildx boots the builder.
if [[ $# -ne 1 || -z "$1" ]]; then
  echo "Usage: $0 IMAGE" >&2
  exit 2
fi

image="$1"
for attempt in 1 2 3 4; do
  echo "Pulling ${image} (attempt ${attempt}/4)"
  if timeout 120s docker pull "$image"; then
    exit 0
  fi
  if [[ "$attempt" == 4 ]]; then
    echo "::error::Failed to pull ${image} after 4 attempts."
    exit 1
  fi
  delay=$((attempt * 5))
  echo "Registry pull failed; retrying in ${delay}s."
  sleep "$delay"
done
