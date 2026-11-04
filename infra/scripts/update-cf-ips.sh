#!/bin/sh
# Prints Cloudflare's IP ranges: one per line for the Hetzner firewall, and a comma list for TRUSTED_PROXY_CIDRS.
set -eu
V4=$(curl -fsS https://www.cloudflare.com/ips-v4)
V6=$(curl -fsS https://www.cloudflare.com/ips-v6)
echo "# firewall (allow tcp/443 from):"
printf '%s\n%s\n' "$V4" "$V6"
echo
echo "# .env (Caddy accepts Cloudflare as its peer; Go trusts only Caddy's fixed address on the proxy network):"
printf 'CLOUDFLARE_CIDRS=%s\n' "$(printf '%s\n%s\n' "$V4" "$V6" | paste -sd' ' -)"
printf 'TRUSTED_PROXY_CIDRS=172.30.0.2/32\n'
