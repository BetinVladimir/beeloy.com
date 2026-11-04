# 08. Инфраструктура, развёртывание, эксплуатация

Исполнитель создаёт **файлы конфигурации и скрипты**; реальные серверы, домены, секреты и аккаунты создаёт человек. Любое действие над внешними системами (деплой, DNS, Cloudflare, Hetzner) — только после явного подтверждения пользователя.

## 1. Топология

```text
Браузер ──► beeloy.com (Cloudflare: DNS proxied, TLS, WAF) ──► Worker «beeloy-web» (Next.js, OpenNext)
                                   │  /api/* и SSR-запросы к данным: fetch https://api-site.beeloy.com
                                   ▼
            api-site.beeloy.com (Cloudflare: DNS proxied, A-запись на IP Hetzner, WAF, rate-limit)
                                   │  TLS Full (strict) + Authenticated Origin Pulls
                                   ▼
Hetzner (1 сервер, Docker Compose, сеть "internal"):
   caddy(443; сертификат Cloudflare Origin CA) → api(8080) ; content-sync(8081) ; postgres(5432) ; redis(6379)
S3-совместимое хранилище (внешнее, приватное): контент (словари i18n, статьи, переводы) и отдельный бакет бэкапов restic
beeloy.org → отдельный Worker «beeloy-org»: витрина, без авторизации; CTA → beeloy.com
Операторская машина: CLI `translator` (вручную) → Ollama и → S3
```
Cloudflare: режим SSL «Full (strict)»; на Caddy установлен **сертификат Cloudflare Origin CA** для `api-site.beeloy.com` (создаёт человек в панели Cloudflare, файлы кладутся на сервер вне репозитория); Authenticated Origin Pulls включён (Caddy требует клиентский сертификат Cloudflare). Cache Rules для `api-site.beeloy.com`: «Bypass cache» по умолчанию; «Respect origin» только для `/api/v1/i18n/*` и `/api/v1/categories`. Для статей, профилей, поиска, Ideas, комментариев, sitemap-entries и /resources — Bypass на обоих hostname; HTML/RSC этих страниц тоже Bypass/no-store. Ответы public и restricted одинаково защищены от устаревшего общего кэша. При выкладке исправления один раз очистить ранее созданные кэши изменяемого контента; это отдельное внешнее действие, не выполняемое обновлением ТЗ.
SSE heartbeat 25 с; фактические лимиты всей цепочки проверяются T05/staging, фиксированное значение таймаута платформы не предполагается.

## 2. Docker Compose (`infra/compose/docker-compose.yml`)

Сервисы и параметры (шаблон; образы закрепить конкретными тегами при реализации):

| Сервис | Образ/сборка | Сеть | Тома | Healthcheck | Лимиты |
|---|---|---|---|---|---|
| `caddy` | `caddy:2` | `edge`, `internal`; порты `443:443` | `./caddy/Caddyfile`, `/etc/caddy/certs` (ro: `origin.pem`, `origin.key`, `cf-origin-pull-ca.pem`), `caddy_data` | `wget -qO- http://localhost:2019/config` (admin на localhost) | mem 128m |
| `api` | сборка `backend/Dockerfile` (multi-stage, `CGO_ENABLED=0`, образ `gcr.io/distroless/static`, пользователь nonroot) команда `api` | `internal` | — | `GET /readyz` | mem 512m, cpus 1 |
| `content-sync` | тот же образ, команда `content-sync run` (UI + статьи) | `internal` | — | `GET :8081/healthz` | mem 128m |
| `migrate` | тот же образ, команда `migrate up`, `restart: "no"` | `internal` | `./migrations` в образ | — | depends_on postgres healthy |
| `postgres` | `postgres:<major>-alpine` | `internal` | `pg_data` | `pg_isready` | shm_size 256m |
| `redis` | `redis:<major>-alpine`, `--appendonly yes --maxmemory 256mb --maxmemory-policy volatile-lru` | `internal` | `redis_data` | `redis-cli ping` | |
| `backup` | `restic/restic` + `infra/backup/backup.sh` по cron-расписанию (`supercronic`) | `internal` | `pg_dump` через `docker exec`-free `pg_dump -h postgres` | лог последнего успеха | |
| `mailpit` | только `compose.dev.yml` | `internal` | | | SMTP :1025, UI :8025 |

`translator` в Compose **не входит**: утилита запускается оператором вручную (`go run ./cmd/translator …` или бинарник из образа `docker run --rm --env-file … beeloy-backend /translator article --id …`) на машине с доступом к Ollama и S3. Ollama в Compose не включается (ТЗ-D18).

Правила: порты postgres/redis **не публикуются**; `restart: unless-stopped`; `stop_grace_period: 30s`; логи драйвер `json-file` с `max-size: 10m, max-file: 5`; `api` стартует после успешного `migrate` (`depends_on: condition: service_completed_successfully`). Секреты — через `env_file: ../../.env` (файл вне git; в репозитории только `.env.example` со всеми ключами из 01 §3 и пустыми/фиктивными значениями).

## 3. Caddyfile (`infra/caddy/Caddyfile`)

```caddyfile
{
    admin localhost:2019
}
api-site.beeloy.com:443 {
    tls /etc/caddy/certs/origin.pem /etc/caddy/certs/origin.key {
        client_auth {
            mode require_and_verify
            trusted_ca_cert_file /etc/caddy/certs/cf-origin-pull-ca.pem
        }
    }
    @api path /api/*
    handle @api {
        reverse_proxy api:8080 {
            flush_interval -1          # SSE
            header_up X-Forwarded-Proto https
            transport http { response_header_timeout 60s }
        }
    }
    handle {
        respond "not found" 404        # на этом хосте только /api/*; страницы сайта обслуживает Worker beeloy.com
    }
    header {
        Strict-Transport-Security "max-age=31536000"
        -Server
    }
}
```
Файрвол Hetzner: входящий 443 только с диапазонов Cloudflare (Worker и браузеры доходят до сервера только через Cloudflare) (список обновляется скриптом `infra/scripts/update-cf-ips.sh`), 22 — только с IP администратора, остальное закрыто. Go доверяет только Caddy: `TRUSTED_PROXY_CIDRS` — фиксированный адрес Caddy в выделенной proxy-сети, не список Cloudflare. Caddy проверяет входящий peer по `CLOUDFLARE_CIDRS`; сохраняет X-Proxy-Secret/X-Real-Client-IP от Worker, Go сверяет секрет. Прямой запрос к api-site без секрета получает 403, кроме GET-only публичных i18n/categories (01 §6); для них Caddy проверяет Cloudflare peer и перезаписывает X-Real-Client-IP проверенным CF-Connecting-IP. Для всех остальных путей исходный Worker IP защищён секретом; Caddy его не заменяет адресом Worker; порт Go наружу закрыт. SSR тоже использует общий transport с секретом, а не обход ingress. Тестировать разные реальные IP и подделку заголовков в полной proxy-топологии.

## 4. Dockerfile бэкенда (`backend/Dockerfile`)

Stage 1 `golang:<ver>`: `go mod download`, `go build -trimpath -ldflags "-s -w" -o /out/api ./cmd/api`, то же для `content-sync`, `migrate`, `translator`. Stage 2 `distroless/static:nonroot`: копировать четыре бинарника и `/migrations`. Единый образ, команда выбирается `CMD`. `ENTRYPOINT` отсутствует (используются полные команды в compose: `["/api"]`, `["/content-sync","run"]`, `["/migrate","up"]`).

`cmd/migrate`: аргументы `up`, `down <n>`, `version`; `golang-migrate` с источником `file:///migrations`; завершает процесс с кодом ≠ 0 при ошибке; не запускает `down` автоматически никогда.

## 5. Cloudflare

DNS (создаёт человек): `beeloy.com` — привязка к Worker (Custom Domain / Route `beeloy.com/*`); `api-site.beeloy.com` — A-запись на IP сервера Hetzner, **proxied (оранжевое облако)**.

- `apps/web/wrangler.jsonc`: `name: "beeloy-web"`, `main: ".open-next/worker.js"`, `compatibility_flags: ["nodejs_compat"]`, `assets` из `.open-next/assets`, маршрут `beeloy.com/*`; переменные `NEXT_PUBLIC_SITE_URL=https://beeloy.com`, `API_ORIGIN=https://api-site.beeloy.com`, `NEXT_PUBLIC_DEFAULT_LOCALE=bg`; секрет `PROXY_SHARED_SECRET` (`wrangler secret put`).
- Весь трафик браузера идёт на `beeloy.com`; `/api/*` обрабатывает прокси-обработчик Next.js (07 «Прокси API»). Прямых обращений браузера к `api-site.beeloy.com` нет.
- Скрипты: `pnpm --dir apps/web build:cf` (`opennextjs-cloudflare build`), `preview:cf`, `deploy:cf` (выполняет человек или CI после подтверждения).
- Запасной вариант (если спайк T05 покажет, что Worker не пропускает SSE потоком): браузер ходит на `api-site.beeloy.com` напрямую; потребует CORS с credentials для `https://beeloy.com`, cookie `Domain=beeloy.com`, `EventSource` с `withCredentials` и правку ТЗ.

## 6. CI (GitHub Actions, `.github/workflows/`)

`ci.yml` на каждый PR и push в `main`:
1. job `go`: `go vet ./...`, `staticcheck` (если установлен), `go test ./... -race` (сервисы Postgres/Redis через testcontainers, runner `ubuntu-latest` имеет Docker).
2. job `web`: `pnpm --dir apps/web install --frozen-lockfile`, `pnpm --dir apps/web lint`, `pnpm --dir apps/web typecheck`, `pnpm --dir apps/web test`, `pnpm --dir apps/web build:cf` (сборка без деплоя), проверка `extract-i18n-keys` (07 §2.3).
3. job `migrations`: поднять Postgres, `migrate up`, `migrate down` до нуля, `migrate up` снова (проверка обратимости).
4. job `e2e` (только main и по метке): compose dev-стек + playwright.

`deploy-backend.yml` (ручной запуск `workflow_dispatch`, окружение `production` с обязательным апрувом): сборка и push образа в реестр (GHCR), SSH на сервер: `docker compose pull && docker compose run --rm migrate && docker compose up -d api content-sync`. `deploy-web.yml` (ручной, с апрувом): `opennextjs-cloudflare deploy` для web и org. Секреты репозитория: `SSH_HOST`, `SSH_KEY`, `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`, `GHCR_TOKEN`. Секретов в логи не выводить.

Порядок релиза при изменении схемы: миграции только **обратно совместимые** (добавление колонок/таблиц с default, без удаления/переименования в том же релизе); сначала `migrate up`, затем новая версия API; удаление устаревшего — отдельным следующим релизом. Откат приложения = выкатить предыдущий образ; `down` миграции в проде вручную по решению человека.

## 7. Бэкапы и восстановление

`infra/backup/backup.sh` создаёт **согласованный набор PostgreSQL + S3**, не только dump. Рабочий порядок для MVP:

1. Включить maintenance: запретить записи контента/API, приостановить content-sync и операторские/CLI-записи S3; дождаться завершения текущих операций. Доступы операторов и момент паузы описать в OPERATIONS. Если не удалось подтвердить остановку всех писателей — snapshot не объявлять согласованным.
2. `pg_dump -Fc`; выгрузить приватный контент S3 (manifest, все ревизии и i18n releases) во временный защищённый каталог; построить inventory ключей, размера и SHA-256. Обычный S3 listing не является независимым бэкапом.
3. Одним restic snapshot сохранить dump, объекты, inventory, номер схемы и backup-set ID в отдельный backup bucket с отдельными правами и шифрованием. Только после успеха возобновить записи и пометить backup successful. При ошибке возобновить сервисы в finally, послать алерт и не объявлять успешный snapshot.
4. Retention: daily 7, weekly 4, monthly 6; фактический максимальный возраст копии до 190 дней, проверить и отразить в Privacy Policy. Секреты restic и доступ на удаление бэкапов отделить от API/content-sync/translator. Версионирование primary S3 полезно, но не заменяет эту независимую копию.

Redis не бэкапится как источник сообщений: история в PostgreSQL. После восстановления очистить эфемерные сессии OTP/лимиты/кэши по процедуре; опубликованные outbox-события без Redis требуют resync клиентов из БД, это не обещание бессрочного replay Streams.

`restore-test.sh` ежемесячно восстанавливает **оба** компонента во временные Postgres и S3, сверяет inventory/hashes, миграции, published_rev/ACL и чтение хотя бы одной статьи по всем её локалям. Негативно проверяет draft/restricted, а также отсутствие тела удалённого комментария. Перед открытием трафика после аварийного восстановления применить deletion-журнал (только user UUID и дата; шифрованный, независимый от старого snapshot) и повторить соответствующие PurgeUser; записи журнала удерживать до удаления последней затрагиваемой резервной копии. Не восстанавливать аккаунт/текст, удалённые после snapshot.

Цели [Т]: RPO 24 ч и RTO 4 ч; это требования к проверке, не достигнутые результаты. При несовместимом S3-провайдере/недостатке места проверка должна выявить блокер, не исключать тексты из бэкапа. До production описать размеры, длительность maintenance и доказанный restore time.

## 8. Наблюдаемость

- Логи JSON в stdout, ротация драйвером Docker.
- `GET /readyz` + внешний uptime-монитор (настраивает человек).
- API экспортирует `GET /metrics` (Prometheus text, только на `internal` сети: на Caddy путь `/api/metrics` НЕ проксируется — маршрут `/metrics` без префикса `/api` слушается на отдельном порту `:9090`): счётчики `http_requests_total{route,status}`, гистограмма `http_request_duration_seconds`, gauge `sse_connections`, `db_pool_in_use`, `i18n_active_version_info`, `outbox_unpublished`, `outbox_oldest_unpublished_seconds`, `stream_pending{group}`. Использовать `github.com/prometheus/client_golang`.
- Алерты (описать в `infra/ALERTS.md`, настраивает человек): 5xx > 2% 5 мин; `readyz` fail > 2 мин; последний успешный sync > 30 мин; самое старое неопубликованное событие outbox > 5 мин или рост `events:dead`; диск > 80%; свежий бэкап старше 36 ч.

## 9. Окружения

| | local (dev) | staging | production |
|---|---|---|---|
| Запуск | `make dev-up` (compose.dev: postgres, redis, mailpit, minio) + `go run ./cmd/api` + `pnpm --dir apps/web dev` | отдельный Compose/сервер (вне MVP-обязательств) | Hetzner + Cloudflare |
| Mailer | Mailpit | SMTP | SMTP |
| S3 | MinIO | отдельный бакет | боевой бакет |
| `APP_ENV` | dev | staging | prod |

`compose.dev.yml` публикует порты postgres/redis/mailpit/minio **только на 127.0.0.1**. Для локальной разработки допускается `LogMailer` и «dev-вход»: эндпоинт `POST /api/v1/dev/login {"email"}` существует **только** при `APP_ENV=dev` (в prod не регистрируется — тест это проверяет).

## 10. Витрина и социальный вход

`apps/org/wrangler.jsonc` задаёт отдельный Worker beeloy-org, домен beeloy.org и SITE_URL для своих canonical; без PROXY_SHARED_SECRET, auth endpoints и session cookie. Сборка, линт и smoke SEO входят в CI; общий JS workspace не обязателен, каждый app имеет свой package.json/lockfile. Монорепо остаётся единым.

Оператор регистрирует Google/Facebook applications и callback `https://beeloy.com/api/v1/auth/oauth/{provider}/callback`. Секреты провайдеров находятся только в API, не в Worker/browser. Тесты используют provider HTTP-заглушки, реальные входы проверяются отдельно до запуска. PROXY_SHARED_SECRET защищает также callback ingress: браузер вызывает beeloy.com, Worker добавляет секрет. Передача нескольких Set-Cookie и Location обязательна.

В локальном окружении разрешены MinIO или S3Mock, если общий integration suite доказывает условную запись PutIfAbsent/CAS. Эмулятор, не поддерживающий эти операции, не подтверждает контракт неизменяемости; нельзя заменить атомарную запись Exists+Put ради зелёного теста.
