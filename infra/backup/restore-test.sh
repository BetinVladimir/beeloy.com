#!/bin/sh
# Monthly restore test of the LATEST consistent snapshot (dump + bucket objects) into throw-away PostgreSQL and a
# throw-away directory standing in for the bucket. A backup counts as valid only after this prints "restore-test: OK".
# Needs docker, restic and sha256sum on the host.
set -eu
: "${RESTIC_REPOSITORY:?}" "${RESTIC_PASSWORD:?}"

NAME="beeloy-restore-test-$$"
OUT="$(mktemp -d)"
FAILS=0
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$OUT"' EXIT
fail() { echo "restore-test: FAIL - $*" >&2; FAILS=$((FAILS + 1)); }
psqlq() { docker exec "$NAME" psql -U postgres -d restore -tAc "$1"; }

restic restore latest --tag beeloy --target "$OUT" >/dev/null
SNAP="$(find "$OUT" -name beeloy.dump -print | head -n 1)"
[ -n "$SNAP" ] || { echo "restore-test: FAIL - no dump in the snapshot"; exit 1; }
DIR="$(dirname "$SNAP")"
[ -d "$DIR/s3" ] && [ -f "$DIR/inventory.sha256" ] || { echo "restore-test: FAIL - the snapshot has no S3 objects (a database-only backup is not valid)"; exit 1; }

# 1. every object matches the inventory (path, size, sha256)
while IFS="$(printf '\t')" read -r path size sum; do
  f="$DIR/s3/$path"
  [ -f "$f" ] || { fail "missing object $path"; continue; }
  [ "$(wc -c < "$f" | tr -d ' ')" = "$size" ] || fail "size differs: $path"
  [ "$(sha256sum "$f" | cut -d' ' -f1)" = "$sum" ] || fail "hash differs: $path"
done < "$DIR/inventory.sha256"

# 2. restore PostgreSQL
docker run -d --name "$NAME" -e POSTGRES_PASSWORD=restore -e POSTGRES_DB=restore postgres:17-alpine >/dev/null
# -h 127.0.0.1: the temporary init server of the image listens on the socket only, so a socket check would pass too early
for _ in $(seq 1 60); do docker exec "$NAME" pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1 && break; sleep 1; done
docker exec -i "$NAME" pg_restore -U postgres -d restore --no-owner < "$DIR/beeloy.dump"

EXPECT="$(cat "$DIR/SCHEMA_VERSION")"
[ "$(psqlq 'SELECT version FROM schema_migrations')" = "$EXPECT" ] || fail "schema version is not $EXPECT"
[ "$(psqlq "SELECT count(*) FROM role_bindings rb JOIN users u ON u.id=rb.user_id WHERE rb.role='admin' AND rb.scope_type='global' AND u.status='active'")" -ge 1 ] || fail "no active global admin"

# 3. publication state and files agree: every live locale has its object with the registered hash
psqlq "SELECT a.id, l.rev, l.locale, l.sha256 FROM article_locales l JOIN articles a ON a.id=l.article_id WHERE a.status='published'" | tr '|' ' ' | \
while read -r id rev loc sum; do
  f="$DIR/s3/articles/$id/rev-$rev/$loc.json"
  [ -f "$f" ] && [ "$(sha256sum "$f" | cut -d' ' -f1)" = "$sum" ] || echo "bad $id $rev $loc"
done > "$OUT/bad-live" || true
[ ! -s "$OUT/bad-live" ] || fail "live article files do not match the database: $(head -n 3 "$OUT/bad-live" | tr '\n' ';')"
[ "$(psqlq "SELECT count(*) FROM article_locales l JOIN articles a ON a.id=l.article_id WHERE a.status<>'published' OR l.rev<>a.published_rev")" = "0" ] || fail "unpublished or stale revisions are live"
PUB="$(psqlq "SELECT count(*) FROM articles WHERE status='published'")"
if [ "$PUB" -ge 1 ]; then
  [ "$(psqlq "SELECT count(*) FROM article_locales l JOIN articles a ON a.id=l.article_id WHERE a.status='published' AND a.visibility='public'")" -ge 1 ] || fail "no public article is readable"
fi

# 4. privacy: nothing deleted is back
[ "$(psqlq "SELECT count(*) FROM comments WHERE status='deleted' AND body<>''")" = "0" ] || fail "deleted comment bodies are present"
[ "$(psqlq "SELECT count(*) FROM users WHERE status='deleted' AND (nickname<>'Deleted user' OR bio<>'')")" = "0" ] || fail "deleted accounts keep personal data"

if [ "$FAILS" -eq 0 ]; then
  echo "restore-test: OK (set $(cat "$DIR/BACKUP_SET"), schema $EXPECT, $(wc -l < "$DIR/inventory.sha256" | tr -d ' ') objects)"
else
  echo "restore-test: FAIL ($FAILS problems)" >&2
  exit 1
fi
echo "NOTE: before opening traffic after a real restore, re-apply the deletion journal (infra/OPERATIONS.md)."
