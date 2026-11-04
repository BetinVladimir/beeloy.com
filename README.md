# Beeloy.com

Сообщество Beeloy: новости и практические статьи на 26 языках, Beeloy Ideas, публичные профили `/{язык}/people/{slug}`,
комментарии, личные сообщения. Вход только по коду из e-mail.

## Состав
| Каталог | Что это |
|---|---|
| `apps/web` | beeloy.com: Next.js + Mantine (Cloudflare Worker), прокси `/api/*` на `api-site.beeloy.com` |
| `apps/org` | beeloy.org: публичная витрина (Worker `beeloy-org`, без входа и cookie) |
| `backend` | Go: `cmd/api`, `cmd/content-sync`, `cmd/migrate`, `cmd/translator` |
| `migrations` | SQL-миграции PostgreSQL (0001–0012) |
| `contracts` | `locales.json` (26 языков), `glossary.json`, `messages/en.json` — общие данные |
| `infra` | Docker Compose, Caddy, бэкапы, инструкции для Cloudflare и эксплуатации |
| `docs` | материалы проекта и ТЗ (`docs/tz`) |

Схема: браузер → `beeloy.com` (Cloudflare, Next.js) → `api-site.beeloy.com` (Cloudflare) → Caddy на Hetzner → Go API.
Тексты статей и переводы лежат в S3 (JSON), в PostgreSQL — состояния, версии и поисковые индексы; публикует модератор.

## Быстрый старт (разработка)
```bash
# бэкенд и зависимости
docker compose -f infra/compose/docker-compose.yml -f infra/compose/compose.dev.yml up -d   # postgres, redis, mailpit, s3, api, content-sync
# фронтенд
cd apps/web && cp .env.example .env.local && pnpm install && pnpm dev                       # http://localhost:3000
cd apps/org && cp .env.example .env.local && pnpm install && pnpm dev                       # http://localhost:3100 (витрина)
```
Коды входа приходят в Mailpit: http://localhost:8025. Первого администратора создаёт
`docker compose … exec api /api bootstrap-admin --email you@example.com`.

## Проверки
```bash
make test-go      # Go: интеграционные тесты на PostgreSQL/Redis/S3 в контейнерах (нужен Docker)
make test-web     # vitest
make lint build
pnpm --dir apps/web test:e2e   # Playwright (нужен запущенный стек из «Быстрого старта»)
```

Подробности: `docs/tz/` (ТЗ и `DONE.md` с результатами), `infra/OPERATIONS.md`, `infra/CLOUDFLARE.md`.
