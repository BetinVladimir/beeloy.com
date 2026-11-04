#!/bin/sh
# Runs the REAL infra/caddy/Caddyfile (TLS + Authenticated Origin Pulls with throw-away certificates) in front of
# (1) an echo upstream, to check which headers Caddy forwards, and (2) a staging API with trusted ingress enabled,
# to check the 403/401 gate, OAuth redirects (Location + Set-Cookie) and unbuffered SSE.
# Needs: docker, openssl, curl, the dev stack of compose.dev.yml (api image, mailpit, fake-oauth, Postgres, Redis).
# It stands in for Cloudflare: the test client plays the Cloudflare edge (client certificate + CF-Connecting-IP).
set -eu
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
W="$(mktemp -d)"
NET=beeloy_ingress_t
SECRET="ingress-check-secret-0123456789abcdef"
PORT=18443
URL="https://api-site.beeloy.com:$PORT"
FAILS=0
cleanup() { docker rm -f ingress-caddy ingress-echo ingress-api >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; rm -rf "$W"; }
trap cleanup EXIT
ok()   { echo "  ok   - $1"; }
fail() { echo "  FAIL - $1" >&2; FAILS=$((FAILS + 1)); }
expect_eq() { if [ "$2" = "$3" ]; then ok "$1"; else fail "$1 (got '$2', want '$3')"; fi; }
expect_has() { case "$2" in *"$3"*) ok "$1";; *) fail "$1 (missing '$3' in: $(echo "$2" | head -c 300))";; esac; }
expect_not() { case "$2" in *"$3"*) fail "$1 (found '$3')";; *) ok "$1";; esac; }

# --- throw-away PKI: origin certificate + the CA whose client certificates are accepted (Authenticated Origin Pulls)
cd "$W"
openssl req -x509 -newkey rsa:2048 -nodes -keyout origin.key -out origin.pem -days 2 -subj "/CN=api-site.beeloy.com" -addext "subjectAltName=DNS:api-site.beeloy.com" 2>/dev/null
openssl req -x509 -newkey rsa:2048 -nodes -keyout pullca.key -out cf-origin-pull-ca.pem -days 2 -subj "/CN=test-origin-pull-ca" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -keyout client.key -out client.csr -subj "/CN=edge" 2>/dev/null
openssl x509 -req -in client.csr -CA cf-origin-pull-ca.pem -CAkey pullca.key -CAcreateserial -out client.pem -days 2 2>/dev/null
chmod 644 *.key *.pem

docker network create --subnet 172.31.0.0/29 "$NET" >/dev/null
caddy() {
  docker rm -f ingress-caddy >/dev/null 2>&1 || true
  docker run -d --name ingress-caddy --network "$NET" --ip 172.31.0.2 -p "127.0.0.1:$PORT:443" \
    -v "$ROOT/infra/caddy/Caddyfile:/etc/caddy/Caddyfile:ro" -v "$W:/etc/caddy/certs:ro" \
    -e "CLOUDFLARE_CIDRS=0.0.0.0/0 ::/0" caddy:2 >/dev/null
  for _ in $(seq 1 30); do curl -sk --max-time 2 -o /dev/null --cert "$W/client.pem" --key "$W/client.key" --resolve "api-site.beeloy.com:$PORT:127.0.0.1" "$URL/x" 2>/dev/null && return 0; sleep 1; done
  echo "caddy did not start:" >&2; docker logs ingress-caddy >&2; exit 1
}
# curl as the Cloudflare edge: client certificate, server certificate pinned
edge() { curl -s --max-time 10 --cacert "$W/origin.pem" --cert "$W/client.pem" --key "$W/client.key" --resolve "api-site.beeloy.com:$PORT:127.0.0.1" "$@"; }

echo "== 1. headers forwarded by the real Caddyfile (echo upstream)"
docker run -d --name ingress-echo --network "$NET" --ip 172.31.0.3 --network-alias api traefik/whoami -port 8080 >/dev/null
caddy
if curl -s --max-time 10 --cacert "$W/origin.pem" --resolve "api-site.beeloy.com:$PORT:127.0.0.1" -o /dev/null "$URL/api/v1/categories" 2>/dev/null; then fail "a request without the Cloudflare client certificate must be refused"; else ok "no client certificate: TLS handshake refused (Authenticated Origin Pulls)"; fi
R="$(edge -H "X-Proxy-Secret: $SECRET" -H "X-Real-Client-IP: 6.6.6.6" -H "CF-Connecting-IP: 203.0.113.7" "$URL/api/v1/i18n/meta")"
expect_has "public GET: client address comes from CF-Connecting-IP" "$R" "X-Real-Client-Ip: 203.0.113.7"
expect_not "public GET: a spoofed X-Real-Client-IP is gone" "$R" "6.6.6.6"
expect_not "public GET: the Worker secret is not forwarded" "$R" "X-Proxy-Secret"
R="$(edge -H "X-Proxy-Secret: $SECRET" -H "X-Real-Client-IP: 198.51.100.9" -H "CF-Connecting-IP: 203.0.113.7" "$URL/api/v1/me")"
expect_has "Worker request: secret reaches the API" "$R" "X-Proxy-Secret: $SECRET"
expect_has "Worker request: the Worker's client address reaches the API" "$R" "X-Real-Client-Ip: 198.51.100.9"
R="$(edge -H "X-Proxy-Secret: $SECRET" -H "CF-Connecting-IP: 203.0.113.7" "$URL/api/v1/i18n/en/extra")"
expect_has "i18n sub-paths are not public (secret kept)" "$R" "X-Proxy-Secret"
expect_eq "paths outside /api are not served" "$(edge -o /dev/null -w '%{http_code}' "$URL/admin")" "404"
expect_has "HSTS is set and the Server header is hidden" "$(edge -D - -o /dev/null "$URL/api/v1/me" | tr 'A-Z' 'a-z')" "strict-transport-security"
expect_not "Server header hidden" "$(edge -D - -o /dev/null "$URL/api/v1/me" | tr 'A-Z' 'a-z')" "server: caddy"
docker rm -f ingress-echo >/dev/null

echo "== 2. staging API behind the same Caddy (trusted ingress enforced)"
IMG="$(docker inspect beeloy-api-1 --format '{{.Config.Image}}')"
set -- $(docker inspect beeloy-api-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -E '^[A-Z0-9_]+=' | grep -vE '^(APP_ENV|PATH|HTTP_ADDR|TRUSTED_PROXY_CIDRS|PROXY_SHARED_SECRET|METRICS_ADDR|OAUTH_DEV_[A-Z_]+)=' | while read -r l; do printf '%s\n%s\n' "-e" "$l"; done)
docker create --name ingress-api --network beeloy_internal "$@" \
  -e APP_ENV=staging -e TERMS_VERSION=1 -e PRIVACY_VERSION=1 -e HTTP_ADDR=:8080 -e METRICS_ADDR=:9090 -e TRUSTED_PROXY_CIDRS=172.31.0.2/32 -e "PROXY_SHARED_SECRET=$SECRET" "$IMG" /api >/dev/null
docker network connect --alias api --ip 172.31.0.4 "$NET" ingress-api
docker start ingress-api >/dev/null
for _ in $(seq 1 40); do [ "$(docker inspect -f '{{.State.Running}}' ingress-api)" = "true" ] && docker exec ingress-api /api healthcheck >/dev/null 2>&1 && break; sleep 1; done
docker exec ingress-api /api healthcheck >/dev/null 2>&1 || { echo "staging api did not start:" >&2; docker logs ingress-api >&2; exit 1; }
caddy

expect_eq "via Caddy without the Worker secret: private API is refused (403)" "$(edge -o /dev/null -w '%{http_code}' "$URL/api/v1/me")" "403"
expect_eq "via Caddy with a wrong secret: refused (403)" "$(edge -o /dev/null -w '%{http_code}' -H 'X-Proxy-Secret: wrong' "$URL/api/v1/me")" "403"
expect_eq "via Caddy with the secret: reaches the API (401 = unauthenticated, not refused)" "$(edge -o /dev/null -w '%{http_code}' -H "X-Proxy-Secret: $SECRET" "$URL/api/v1/me")" "401"
expect_eq "public dictionary meta needs no secret" "$(edge -o /dev/null -w '%{http_code}' "$URL/api/v1/i18n/meta")" "200"
expect_eq "public categories need no secret" "$(edge -o /dev/null -w '%{http_code}' "$URL/api/v1/categories")" "200"
expect_eq "articles list is NOT public without the secret" "$(edge -o /dev/null -w '%{http_code}' "$URL/api/v1/articles")" "403"
# straight to the API container (a peer that is not Caddy): refused even with the right secret, even for public GETs
docker run --rm --network beeloy_internal curlimages/curl -s -o /dev/null -w '%{http_code}' -H "X-Proxy-Secret: $SECRET" http://ingress-api:8080/api/v1/me > "$W/direct1" || true
docker run --rm --network beeloy_internal curlimages/curl -s -o /dev/null -w '%{http_code}' http://ingress-api:8080/api/v1/i18n/meta > "$W/direct2" || true
expect_eq "direct to the API (not via Caddy) with the secret: refused" "$(cat "$W/direct1")" "403"
expect_eq "direct to the API: even the public GET is refused" "$(cat "$W/direct2")" "403"

echo "== 3. OAuth redirect: Location and Set-Cookie survive Caddy"
H="$(edge -D - -o /dev/null -H "X-Proxy-Secret: $SECRET" "$URL/api/v1/auth/oauth/google/start?locale=en&return_to=%2Fen")"
expect_has "302 to the provider" "$H" "302"
expect_has "Location header intact (the real Google endpoint, state and PKCE challenge)" "$(echo "$H" | tr 'A-Z' 'a-z')" "location: https://accounts.google.com/o/oauth2/v2/auth?"
expect_has "PKCE challenge in Location" "$(echo "$H" | tr 'A-Z' 'a-z')" "code_challenge_method=s256"
expect_has "flow cookie set, HttpOnly" "$(echo "$H" | tr 'A-Z' 'a-z')" "bl_oauth_flow="
expect_has "flow cookie HttpOnly" "$(echo "$H" | tr 'A-Z' 'a-z' | grep bl_oauth_flow)" "httponly"

echo "== 4. SSE through Caddy is not buffered"
# the staging API cannot send mail to Mailpit (plain SMTP is a dev-only mode), so the sign-in goes through the dev API;
# its session (same database) is then presented through Caddy to the staging API
EMAIL="ingress-$$@example.com"
JSON='Content-Type: application/json'
DEV="http://localhost:8080"
CSRF='-H Origin:http://localhost:3000 -H X-Requested-With:beeloy'
# shellcheck disable=SC2086
curl -s -o /dev/null $CSRF -H "$JSON" -d "{\"email\":\"$EMAIL\",\"locale\":\"en\"}" "$DEV/api/v1/auth/otp/request"
CODE=""
for _ in $(seq 1 20); do
  CODE="$(python3 - "$EMAIL" <<'PY'
import json, re, sys, urllib.request
base = "http://localhost:8025/api/v1"
for m in json.load(urllib.request.urlopen(base + "/messages"))["messages"]:
    if any(t["Address"] == sys.argv[1] for t in m["To"]):
        text = json.load(urllib.request.urlopen(base + "/message/" + m["ID"]))["Text"]
        c = re.search(r"\b(\d{6})\b", text)
        if c:
            print(c.group(1))
        break
PY
)"
  [ -n "$CODE" ] && break; sleep 0.5
done
[ -n "$CODE" ] || fail "no sign-in code arrived in Mailpit"
# shellcheck disable=SC2086
curl -s -o /dev/null -c "$W/jar" $CSRF -H "$JSON" -d "{\"email\":\"$EMAIL\",\"code\":\"$CODE\",\"locale\":\"en\",\"accept_terms\":true}" "$DEV/api/v1/auth/otp/verify"
SESSION="$(awk '$6=="bl_session"{print $7}' "$W/jar")"
[ -n "$SESSION" ] || fail "no session cookie from the dev API"
expect_eq "the session is accepted by the staging API through Caddy" "$(edge -o /dev/null -w '%{http_code}' -H "X-Proxy-Secret: $SECRET" -H "Cookie: bl_session=$SESSION" "$URL/api/v1/me")" "200"
edge -N --max-time 8 -H "X-Proxy-Secret: $SECRET" -H "Cookie: bl_session=$SESSION" -H "Accept: text/event-stream" "$URL/api/v1/events" > "$W/sse" 2>/dev/null &
SSE_PID=$!
sleep 2   # the heartbeat is 25 s: anything seen now was flushed immediately, not buffered
EV="$(cat "$W/sse")"
kill "$SSE_PID" 2>/dev/null || true
wait "$SSE_PID" 2>/dev/null || true
expect_has "the first event arrives within 2 s while the stream is still open" "$EV" "event: ready"

echo
if [ "$FAILS" -eq 0 ]; then echo "check-ingress: OK"; else echo "check-ingress: FAIL ($FAILS)" >&2; exit 1; fi
