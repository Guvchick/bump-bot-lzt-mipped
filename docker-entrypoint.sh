#!/bin/sh
set -e

# The /data dir is usually a bind mount created by Docker as root, so the
# unprivileged 'bot' user can't write the SQLite file there (SQLITE_CANTOPEN /
# error 14). Fix ownership while we're still root, then drop privileges.
mkdir -p /data
chown -R bot:bot /data

exec su-exec bot:bot /app/bot "$@"
