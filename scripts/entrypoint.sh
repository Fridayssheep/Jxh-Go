#!/bin/sh
# Docker entrypoint that handles data directory permissions
# Runs as root initially, chowns /app/data to the user, then drops privileges via gosu

set -e

# If running as root, fix permissions and drop to appuser
if [ "$(id -u)" = "0" ]; then
    # Determine target UID/GID
    TARGET_UID="${PUID:-10001}"
    TARGET_GID="${PGID:-10001}"

    # Ensure data directory exists and has correct ownership
    mkdir -p /app/data /app/data/cache
    chown -R "${TARGET_UID}:${TARGET_GID}" /app/data

    # Update appuser to match host UID/GID if different
    CURRENT_UID=$(id -u appuser 2>/dev/null || echo "10001")
    CURRENT_GID=$(id -g appuser 2>/dev/null || echo "10001")

    if [ "${TARGET_UID}" != "${CURRENT_UID}" ] || [ "${TARGET_GID}" != "${CURRENT_GID}" ]; then
        # Modify appuser UID/GID to match host user
        groupmod -o -g "${TARGET_GID}" appuser 2>/dev/null || true
        usermod -o -u "${TARGET_UID}" -g "${TARGET_GID}" appuser 2>/dev/null || true
    fi

    # Drop privileges and execute as appuser
    exec gosu appuser "$@"
fi

# Not running as root, just exec
exec "$@"
