# infra/scripts — вспомогательные скрипты

`update-cf-ips.sh` (диапазоны Cloudflare для файрвола Hetzner и `TRUSTED_PROXY_CIDRS`; 443 открыт только им).

`check-ingress.sh` запускает настоящий `infra/caddy/Caddyfile` на одноразовых сертификатах (TLS + Authenticated Origin Pulls) перед echo-сервером и перед staging-API и проверяет заголовки, ворота 403/401, `Location`/`Set-Cookie` и SSE. Нужны docker, openssl, curl и dev-стек.

## Требования

- `docs/tz/08-infrastructure.md §3`

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
