# infra/caddy — обратный прокси

`Caddyfile` для хоста `api-site.beeloy.com`: TLS-сертификат Cloudflare Origin CA на сервере Hetzner + Authenticated Origin Pulls; `/api/*` → `api:8080`, `flush_interval -1` для SSE; остальное — 404. Сертификаты и ключи хранятся на сервере вне репозитория (`/etc/caddy/certs`).

## Требования

- `docs/tz/08-infrastructure.md §3`

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
