# Beeloy

Сообщество осознанного бизнеса (миссия — `docs/ Mission.md`). Два сайта:

- **beeloy.com** — сообщество: новости и практические статьи на 26 языках, Beeloy Ideas, публичные профили
  `/{язык}/people/{slug}`, комментарии, личные сообщения. Вход по коду из e-mail; Google и Facebook реализованы, но
  выключены, пока не заданы ключи и `NEXT_PUBLIC_SOCIAL_PROVIDERS`.
- **beeloy.org** — публичная витрина: о проекте, миссия, правила, контакт (`info@beeloy.org`). Без входа и cookie.

Юридические документы (Terms, Privacy, Cookies) ведутся на `https://legal.beeloy.org/{язык}/…`; страницы `/legal/*`
на beeloy.com перенаправляют туда.

## Схема

```
браузер ─► beeloy.com (Cloudflare Worker, Next.js) ─► api-site.beeloy.com (Cloudflare) ─► Caddy (Hetzner) ─► Go API
                                                                                                   │
                                          PostgreSQL · Redis · S3 (тексты статей и переводы, JSON) ┘
beeloy.org (Worker beeloy-org) ─► публичный словарь API
```

- Тексты статей, ревизии и переводы лежат в S3 и не перезаписываются; в PostgreSQL — состояния, версии, поисковые
  индексы. Публикует модератор. **Content Sync** переносит S3 → PostgreSQL.
- Go принимает `/api/v1/*` только от Caddy с общим секретом Worker (`TRUSTED_PROXY_CIDRS`, `PROXY_SHARED_SECRET`).
- События между сервисами идут через outbox и Redis Streams.

## Структура

| Путь | Что это |
|---|---|
| `apps/web` | beeloy.com: Next.js 16 + Mantine на Cloudflare Workers (OpenNext), прокси `/api/*`; тесты vitest и Playwright (`e2e/`) |
| `apps/org` | beeloy.org: витрина, Worker `beeloy-org`; отдельный `package.json` и lockfile |
| `backend/cmd/api` | HTTP API, фоновые задания; подкоманды `bootstrap-admin`, `outbox`, `run-job`, `replay-deletions`, `healthcheck` |
| `backend/cmd/content-sync` | синхронизация S3 → PostgreSQL (словари и статьи); `run`, `once`, `rollback`, `seed-ui`, `status` |
| `backend/cmd/migrate` | применение SQL-миграций (`up`, `down <n>`, `version`) |
| `backend/cmd/translator` | CLI переводов через Ollama (словари и статьи, без перезаписи) |
| `backend/internal/` | доменные пакеты: `identity` (вход, OTP, OAuth, slug), `acl`, `articles`, `ideas`, `comments`, `messenger`, `moderation`, `privacy`, `search`, `seo`, `i18nstore`, `contentsync`, `translator`, `registry`, `audit`, `jobs`, `admin`; `platform/` — db, httpx, redis, s3store, outbox, ratelimit, maintenance и др.; `e2e` — сквозные тесты на контейнерах |
| `migrations` | SQL-миграции PostgreSQL (0001–0013) |
| `contracts` | общие данные: `locales.json` (26 языков), `glossary.json`, `messages/en.json` (эталонный словарь; копии разносит `make sync-locales`) |
| `infra/compose` | Docker Compose: прод (`docker-compose.yml`) и разработка (`compose.dev.yml`: MinIO, Mailpit, fake-oauth) |
| `infra/caddy` | `Caddyfile`: TLS Origin CA, Authenticated Origin Pulls, правила доверенного ingress |
| `infra/backup` | `backup.sh` (PostgreSQL + S3 с заморозкой записи, restic), `restore-test.sh`, образ с restic и rclone |
| `infra/scripts` | `update-cf-ips.sh`, `check-ingress.sh` (проверка настоящего Caddyfile) |
| `infra/dev/fake-oauth` | локальная заглушка Google/Facebook для разработки и e2e, **не для развёртывания** |
| `.github/workflows` | `ci.yml`, `deploy-web.yml` (`app: web\|org`), `deploy-backend.yml` |
| `docs` | материалы проекта и ТЗ (`docs/tz`: требования, `DONE.md` — результаты, `CONFORMANCE.md` — сверка) |

## Быстрый старт (разработка)

Нужны Docker, Go, Node и pnpm.

```bash
# бэкенд и зависимости: postgres, redis, mailpit, minio, fake-oauth, api, content-sync
export POSTGRES_PASSWORD=beeloy POSTGRES_USER=beeloy POSTGRES_DB=beeloy   # при занятом 5432: POSTGRES_HOST_PORT=5543
make dev-up
# фронтенд
cd apps/web && cp .env.example .env.local && pnpm install && pnpm dev     # http://localhost:3000
cd apps/org && cp .env.example .env.local && pnpm install && pnpm dev     # http://localhost:3100
```

- Коды входа приходят в Mailpit: http://localhost:8025. MinIO: S3 `127.0.0.1:9000`, консоль `9001`.
- Первый администратор: `docker compose … exec api /api bootstrap-admin --email you@example.com`.
- В dev `content-sync` публикует шаблонный релиз словарей (`SEED_UI_TEMPLATE=true`); реальные переводы
  выпускает `translator`.
- Соцвход в dev: `NEXT_PUBLIC_SOCIAL_PROVIDERS=google,facebook` в `apps/web/.env.local`; провайдером служит
  `fake-oauth` (`localhost:4010`).

## Проверки

```bash
make test-go                      # Go на Postgres/Redis/S3Mock в контейнерах (нужен Docker)
TEST_S3_ENDPOINT=http://127.0.0.1:9000 TEST_S3_ACCESS_KEY=dev TEST_S3_SECRET_KEY=dev-minio-secret \
  go test ./... -race             # (в backend/) то же на MinIO из dev-стека
make test-web                     # vitest: apps/web и apps/org
make lint build
pnpm --dir apps/web test:e2e      # Playwright на запущенном dev-стеке: сообщество, статьи, словари, бэкап, соцвход, a11y
sh infra/scripts/check-ingress.sh # настоящий Caddyfile: AOP, доверенный ingress, SSE (нужен dev-стек)
```

Подробности: `docs/tz/` (ТЗ, `DONE.md`, `CONFORMANCE.md`), `infra/OPERATIONS.md`, `infra/CLOUDFLARE.md`, `infra/ALERTS.md`.
