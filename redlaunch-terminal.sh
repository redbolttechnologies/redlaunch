#!/usr/bin/env sh
# Open an interactive shell in the running Redlaunch container.
# Usage:
#   ./redlaunch-terminal.sh [command ...]
# Examples:
#   ./redlaunch-terminal.sh
#   ./redlaunch-terminal.sh -c 'sqlite3 /data/redlaunch.db ".tables"'
set -eu

container="${REDLAUNCH_CONTAINER:-redbolt-redlaunch}"

if ! docker inspect --format '{{.State.Running}}' "$container" 2>/dev/null | grep -q true; then
  echo "redlaunch-terminal: container '$container' is not running (override with REDLAUNCH_CONTAINER=...)." >&2
  exit 1
fi

exec docker exec -it "$container" /bin/sh "$@"
