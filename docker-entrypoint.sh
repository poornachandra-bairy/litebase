#!/bin/sh
# Container entrypoint for Litebase.
#
# The one thing that reliably breaks a containerised deployment is the data
# volume: an orchestrator such as Coolify may mount a host directory owned by
# root, which the unprivileged application user then cannot write to. So the
# container starts as root purely to repair that ownership, then drops to the
# litebase user before exec'ing the server. The application itself never runs
# with elevated privileges.

set -e

DATA_DIR="${LITEBASE_DATA_DIR:-/data}"

if [ "$(id -u)" = "0" ]; then
    mkdir -p "$DATA_DIR"

    # Only chown when the owner is actually wrong. On a large existing data
    # directory a recursive chown is slow, and repeating it on every restart
    # would delay startup for no reason.
    owner="$(stat -c '%u' "$DATA_DIR" 2>/dev/null || echo 0)"
    if [ "$owner" != "10001" ]; then
        echo "litebase: taking ownership of $DATA_DIR" >&2
        chown -R litebase:litebase "$DATA_DIR" || \
            echo "litebase: could not change ownership of $DATA_DIR; continuing" >&2
    fi

    exec su-exec litebase:litebase "$@"
fi

# Already unprivileged, which is the case when the image is run with a
# --user flag or when no ownership repair is needed.
exec "$@"
