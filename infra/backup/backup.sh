#!/bin/sh
# Consistent backup of PostgreSQL + the private content bucket (articles, revisions, i18n releases) into one
# encrypted restic snapshot in a SEPARATE bucket. Redis is not backed up (OTP/limits/cache are ephemeral).
#
# Order: freeze writes -> S3 inventory -> pg_dump -> copy objects -> S3 inventory again -> compare -> snapshot -> resume.
# If the two inventories differ, some writer was not stopped: the set is NOT consistent and no snapshot is made.
set -eu
: "${RESTIC_REPOSITORY:?}" "${RESTIC_PASSWORD:?}" "${DATABASE_URL:?}" "${REDIS_URL:?}"
: "${S3_BUCKET:?}" "${S3_ACCESS_KEY:?}" "${S3_SECRET_KEY:?}"
MAINT_WAIT="${MAINT_WAIT:-20}"   # seconds for in-flight requests to finish after the freeze
MAINT_TTL="${MAINT_TTL:-3600}"   # the freeze expires by itself if this script dies

export RCLONE_CONFIG_SRC_TYPE=s3 RCLONE_CONFIG_SRC_PROVIDER=Other \
  RCLONE_CONFIG_SRC_ENDPOINT="${S3_ENDPOINT:-}" RCLONE_CONFIG_SRC_REGION="${S3_REGION:-}" \
  RCLONE_CONFIG_SRC_ACCESS_KEY_ID="$S3_ACCESS_KEY" RCLONE_CONFIG_SRC_SECRET_ACCESS_KEY="$S3_SECRET_KEY"
SRC="src:${S3_BUCKET}/${S3_PREFIX:-}"

WORK="$(mktemp -d)"
cleanup() {
  redis-cli -u "$REDIS_URL" DEL maintenance >/dev/null 2>&1 || echo "WARNING: could not clear the maintenance flag; it expires in at most ${MAINT_TTL}s" >&2
  rm -rf "$WORK"
}
trap cleanup EXIT

SET_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
redis-cli -u "$REDIS_URL" SET maintenance 1 EX "$MAINT_TTL" >/dev/null
echo "writes frozen (set $SET_ID); waiting ${MAINT_WAIT}s for in-flight requests"
sleep "$MAINT_WAIT"

mkdir -p "$WORK/s3"
rclone lsf -R --files-only --format "pst" --separator "	" "$SRC" | sort > "$WORK/inventory.before"
pg_dump --format=custom --no-owner "$DATABASE_URL" > "$WORK/beeloy.dump"
SCHEMA="$(psql "$DATABASE_URL" -tAc 'SELECT version FROM schema_migrations')"
rclone copy "$SRC" "$WORK/s3" --checksum
rclone lsf -R --files-only --format "pst" --separator "	" "$SRC" | sort > "$WORK/inventory.after"

if ! cmp -s "$WORK/inventory.before" "$WORK/inventory.after"; then
  echo "FAIL: the bucket changed during the backup; some writer (operator CLI, another service) was not stopped. No snapshot made." >&2
  exit 1
fi
# independent inventory of what was actually copied: path, size, sha256
( cd "$WORK/s3" && find . -type f | sort | while read -r f; do printf '%s\t%s\t%s\n' "${f#./}" "$(wc -c < "$f" | tr -d ' ')" "$(sha256sum "$f" | cut -d' ' -f1)"; done ) > "$WORK/inventory.sha256"
COPIED="$(wc -l < "$WORK/inventory.sha256" | tr -d ' ')"
LISTED="$(wc -l < "$WORK/inventory.after" | tr -d ' ')"
if [ "$COPIED" != "$LISTED" ]; then
  echo "FAIL: copied $COPIED objects but the bucket lists $LISTED" >&2
  exit 1
fi
printf '%s\n' "$SET_ID" > "$WORK/BACKUP_SET"
printf '%s\n' "$SCHEMA" > "$WORK/SCHEMA_VERSION"

restic snapshots >/dev/null 2>&1 || restic init
restic backup "$WORK" --tag beeloy --tag "set:$SET_ID" --host beeloy
restic forget --tag beeloy --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune
echo "backup ok: set=$SET_ID objects=$COPIED schema=$SCHEMA $(date -u +%FT%TZ)"
