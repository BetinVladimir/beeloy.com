# infra

- `compose/` — Docker Compose (`docker-compose.yml` + `compose.dev.yml`)
- `caddy/Caddyfile` — вход для `api-site.beeloy.com` (сертификат Cloudflare Origin CA на сервере Hetzner)
- `backup/` — бэкапы PostgreSQL (restic) и проверка восстановления
- `scripts/update-cf-ips.sh` — диапазоны Cloudflare для файрвола и `TRUSTED_PROXY_CIDRS`
- `CLOUDFLARE.md` — ручная настройка Cloudflare; `OPERATIONS.md` — эксплуатация; `ALERTS.md` — алерты

## Локальная разработка
```bash
cp .env.example .env   # необязательно: compose.dev.yml задаёт все значения для разработки
docker compose -f infra/compose/docker-compose.yml -f infra/compose/compose.dev.yml up -d   # postgres, redis, mailpit, s3, api, content-sync
cd apps/web && cp .env.example .env.local && pnpm install && pnpm dev                      # http://localhost:3000
```
Почта (коды входа) — в Mailpit: http://localhost:8025. Прокси `/api/*` в `pnpm dev` идёт на `API_ORIGIN=http://localhost:8080`.

## Проверка защиты origin
Без клиентского сертификата Cloudflare Caddy отвечает отказом TLS-рукопожатия:
`curl -v --resolve api-site.beeloy.com:443:<IP сервера> https://api-site.beeloy.com/api/v1/i18n/meta` → ошибка «certificate required».
