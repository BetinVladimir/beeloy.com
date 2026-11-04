# 01. Соглашения проекта

## 1. Структура монорепозитория (создать буквально)

```text
beeloy.com/
├─ apps/
│  ├─ web/                    # Next.js/Mantine: сообщество beeloy.com
│  └─ org/                    # Next.js: публичная витрина beeloy.org
├─ contracts/                 # общие данные (не JS-пакет): locales.json, glossary.json, messages/en.json
├─ backend/                   # единый Go-модуль: module beeloy.com/backend
│  ├─ cmd/
│  │  ├─ api/                 # main.go — HTTP API (+ relay, consumers, jobs)
│  │  ├─ content-sync/        # main.go — синхронизация S3 → PostgreSQL
│  │  ├─ translator/          # main.go — CLI перевода (Ollama), запуск вручную
│  │  └─ migrate/             # main.go — миграции
│  └─ internal/
│     ├─ config/  apperr/
│     ├─ platform/            # db, redis, httpx, logx, ids, crypto, pagination, ratelimit, mail, s3store, outbox
│     ├─ identity/  acl/  audit/  registry/
│     ├─ articles/  search/  ideas/  comments/
│     ├─ messenger/  moderation/  privacy/
│     ├─ i18nstore/  jobs/  contentsync/  translator/
│     └─ e2e/                 # сквозные негативные тесты безопасности
├─ migrations/                # NNNN_name.up.sql / NNNN_name.down.sql
├─ infra/
│  ├─ compose/                # docker-compose.yml, compose.dev.yml, .env.example
│  ├─ caddy/                  # Caddyfile
│  ├─ backup/                 # backup.sh, restore-test.sh
│  └─ scripts/                # update-cf-ips.sh
├─ scripts/                   # extract-i18n-keys.ts, sync-locales
├─ .github/workflows/         # ci.yml, deploy-*.yml
├─ docs/                      # материалы + tz/
├─ .editorconfig, .gitignore   # apps/web/package.json и apps/org/package.json; общий контент contracts/
└─ Makefile
```

`Makefile` цели: `dev-up`, `dev-down`, `migrate-up`, `migrate-down`, `test-go`, `test-web`, `lint`, `build`.

## 2. Слои внутри Go-пакета фичи

Каждый пакет фичи (`ideas`, `messenger`, …) содержит файлы:

| Файл | Роль |
|---|---|
| `model.go` | Структуры данных и константы. Без обращений к БД. |
| `repo.go` | Тип `Repo{db *pgxpool.Pool}`; **только** SQL. Методы принимают `ctx` и, если нужна транзакция, `pgx.Tx` через интерфейс `DBTX`. |
| `service.go` | Тип `Service`; бизнес-логика, проверки прав (через `acl.Authorizer`), транзакции, аудит. |
| `handler.go` | Тип `Handler`; разбор HTTP, валидация формы, вызов `Service`, формирование ответа. Метод `Routes(r chi.Router)`. |
| `*_test.go` | Тесты. |

Правила: `handler` не содержит SQL и бизнес-правил; `repo` не содержит проверок прав; `service` не знает про HTTP.

```go
// internal/platform/db/db.go
type DBTX interface {
    Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
// WithTx выполняет fn в транзакции; commit при nil, rollback при ошибке/панике.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error
```

## 3. Конфигурация (ENV)

Загружается один раз в `config.Load() (*Config, error)`; отсутствие обязательной переменной — ошибка старта. Значения не логируются.

| Переменная | Обяз. | Пример / значение по умолчанию | Назначение |
|---|---|---|---|
| `APP_ENV` | да | `dev`/`staging`/`prod` | режим; в `dev` cookie `Secure=false` |
| `HTTP_ADDR` | нет | `:8080` | адрес API |
| `PUBLIC_BASE_URL` | да | `https://beeloy.com` | для ссылок и Origin |
| `ALLOWED_ORIGINS` | да | `https://beeloy.com` | CSV, проверка Origin |
| `DATABASE_URL` | да | `postgres://…` | PostgreSQL |
| `DB_MAX_CONNS` | нет | `20` | пул |
| `REDIS_URL` | да | `redis://redis:6379/0` | Redis |
| `OTP_HMAC_SECRET` | да | ≥32 байт base64 | HMAC кодов OTP |
| `SMTP_HOST`,`SMTP_PORT`,`SMTP_USER`,`SMTP_PASS`,`SMTP_FROM` | да | | почта |
| `S3_ENDPOINT`,`S3_REGION`,`S3_BUCKET`,`S3_ACCESS_KEY`,`S3_SECRET_KEY`,`S3_PREFIX` | api, content-sync, translator | `S3_PREFIX=` пустой или `prod/` | S3-хранилище контента. Права ключей разные: content-sync — только чтение; api и translator — чтение и запись (отдельные ключи для каждого процесса) |
| `SYNC_INTERVAL_UI` | нет | `5m` | опрос словарей |
| `SYNC_INTERVAL_ARTICLES` | нет | `1m` | индексация статей |
| `OLLAMA_URL` | translator (CLI) | `http://gpu-host:11434` | адрес Ollama |
| `OLLAMA_MODEL` | translator | имя модели Ollama | задаётся владельцем, в код не зашивается |
| `TRANSLATOR_REQUEST_TIMEOUT` | нет | `10m` | таймаут одного запроса |
| `GOOGLE_CLIENT_ID`,`GOOGLE_CLIENT_SECRET`,`FACEBOOK_CLIENT_ID`,`FACEBOOK_CLIENT_SECRET` | api в prod | секреты не в git | социальный вход; значения и зарегистрированные callback URL готовит оператор |
| `TERMS_VERSION`,`PRIVACY_VERSION` | да | `2026-11-04` | версии документов, записываемые в `user_agreements` |
| `LOG_LEVEL` | нет | `info` | |
| `TRUSTED_PROXY_CIDRS` | да (prod) | адрес/подсеть только Caddy в выделенной proxy-сети | непосредственные доверенные peer Go; это не диапазоны Cloudflare |
| `CLOUDFLARE_CIDRS` | Caddy (prod) | актуальный список Cloudflare | допустимые peer Caddy и firewall; не смешивать с TRUSTED_PROXY_CIDRS |
| `PROXY_SHARED_SECRET` | да (prod) | случайная строка ≥ 32 байт | секрет, который Worker `beeloy.com` добавляет в заголовок `X-Proxy-Secret`; при совпадении API доверяет заголовку `X-Real-Client-IP` |

Структура S3 — в 06 §1 (контент) и 04 §1 (статьи). Frontend ENV (`apps/web`): `NEXT_PUBLIC_SITE_URL`, `API_ORIGIN=https://api-site.beeloy.com` (адрес API для SSR и для прокси `/api/*`; для dev — `http://localhost:8080`), `PROXY_SHARED_SECRET` (секрет Worker, тот же, что у API), `NEXT_PUBLIC_DEFAULT_LOCALE=bg`.

## 4. Формат HTTP API

- Префикс `/api/v1`. Только JSON `application/json; charset=utf-8` (кроме SSE, raw i18n, file export и OAuth redirects с Location/Set-Cookie).
- Успех: `{"data": <объект|массив>}`; списки: `{"data":[…],"next_cursor":"<строка|null>"}`.
- Нет тела: `204`.
- Ошибка (единственный формат):
```json
{"error":{"code":"validation_failed","message":"human readable, English","fields":{"title":"too_short"}}}
```
`fields` присутствует только для `validation_failed`; значения — машинные коды: `required`, `too_short`, `too_long`, `invalid_format`, `invalid_value`, `taken`, `reserved`.

Таблица кодов (`internal/apperr`):

| code | HTTP | Когда |
|---|---|---|
| `bad_request` | 400 | не разобран JSON, неверные query-параметры |
| `invalid_code` | 400 | неверный/просроченный OTP |
| `unauthorized` | 401 | нет сессии |
| `forbidden` | 403 | нет права, неверный Origin/CSRF, аккаунт заблокирован |
| `not_found` | 404 | нет объекта **или** нет права его видеть (не раскрывать существование) |
| `conflict` | 409 | нарушение уникальности/состояния |
| `validation_failed` | 422 | ошибки полей |
| `rate_limited` | 429 | лимит; заголовок `Retry-After` в секундах |
| `payload_too_large` | 413 | тело > 1 МиБ |
| `unavailable` | 503 | Redis/БД/зависимость недоступна там, где fail-closed |
| `internal` | 500 | остальное; детали только в логах |

```go
// internal/apperr/apperr.go
type Error struct {
    Code    string
    HTTP    int
    Message string
    Fields  map[string]string // optional
    Err     error             // wrapped cause, never sent to client
}
func (e *Error) Error() string
func (e *Error) Unwrap() error
func New(code string, httpStatus int, msg string) *Error
func Validation(fields map[string]string) *Error
var ErrNotFound, ErrForbidden, ErrUnauthorized, ErrConflict, ErrRateLimited *Error // фабрики-заготовки
// httpx.WriteError(w, r, err) — приводит любую ошибку к *Error; неизвестные -> internal + slog.Error.
```

## 5. Пагинация (keyset)

`GET …?limit=20&cursor=…`; `limit` по умолчанию 20, максимум 50 (значения вне диапазона → 400). Курсор непрозрачен: `base64url(JSON{"t":"<RFC3339Nano>","id":"<uuid>"})`. Сортировка `(created_at DESC, id DESC)`; условие страницы: `(created_at, id) < ($t, $id)`. Берётся `limit+1` строк; если получено `limit+1` — есть следующая страница. Для сортировки по другому полю (голоса) курсор расширяется полем `v` (int) — описано в месте использования.

```go
// internal/platform/pagination
type Cursor struct { T time.Time `json:"t"`; ID uuid.UUID `json:"id"`; V *int64 `json:"v,omitempty"` }
func Encode(c Cursor) string
func Decode(s string) (Cursor, error) // пустая строка -> нулевой Cursor, nil
func ParseLimit(q url.Values) (int, error)
```

## 6. Middleware API (порядок)

1. `RequestID` — читает/создаёт `X-Request-Id`, кладёт в контекст и логи.
2. `Recoverer` — паника → `internal`.
3. `AccessLog` — slog: метод, путь (без query), статус, длительность, request_id, user_id (если есть). Тела не логируются.
4. `RealIP` и доверенный ingress: Go принимает внешний `/api/v1/*` в prod только от Caddy (`RemoteAddr` в `TRUSTED_PROXY_CIDRS`) и с корректным `X-Proxy-Secret` (constant-time compare). Исключение: GET-only /api/v1/i18n/meta, /api/v1/i18n/{locale} и /api/v1/categories допускают публичное чтение через Caddy без секрета; в этом случае Caddy сам перезаписывает X-Real-Client-IP проверенным CF-Connecting-IP от peer из CLOUDFLARE_CIDRS. Другие пути/методы исключения не получают. Для запроса с секретом клиентский IP — валидный `X-Real-Client-IP`, подписанный общим секретом Worker; отсутствие/неверный секрет → 403, отсутствующий/невалидный IP → 400. Прямой клиент не может выбирать IP заголовком. `CF-Connecting-IP` на стороне Go не используется; нормализация выполняется Caddy. Публичные i18n/categories вообще не загружают сессию и игнорируют cookie. Worker и Caddy не логируют секреты. Локальный dev отдельно допускает прямой loopback без этих заголовков; тест prod не допускает dev-исключение.
5. `SecurityHeaders` — `X-Content-Type-Options: nosniff`, `Referrer-Policy: strict-origin-when-cross-origin`, `Cache-Control: private, no-store` для изменяемого контента, профилей, Ideas, поиска, комментариев, resource-ссылок, sitemap-entries, identity и приватных маршрутов. Исключения: i18n (версионирование/ETag, 300 с) и categories (300 с). SSE — `private, no-store, no-transform`. Ошибки и ответы с Set-Cookie не кэшируются.
6. `MaxBody(1<<20)`.
7. `LoadSession` — если есть cookie `bl_session`, подгружает `Subject`; на публичных запросах cookie отсутствует по контракту клиента. При скользящем продлении обновляет и БД, и cookie через SetCookie; такой ответ всегда no-store. Невалидная/отсутствующая cookie не создаёт Subject.
8. `CSRF` — OAuth callback проверяет state/flow-cookie (03 §8); для остальных небезопасных методов: `Origin` ∈ `ALLOWED_ORIGINS` и `X-Requested-With == beeloy`, иначе `403 forbidden`.
9. Группы: `RequireAuth` (иначе 401), rate-limit по группе.

## 7. Логирование

`slog` JSON в stdout. Обязательные поля: `time`, `level`, `msg`, `request_id`. Запрещено логировать: email целиком (только `a***@domain`), OTP, токены, cookie, тело сообщений, значения секретов, OAuth code/state/PKCE verifier и токены провайдеров.

## 8. Тесты (общие правила)

- Репозитории и сервисы тестируются на реальных Postgres и Redis через `testcontainers-go`; миграции применяются из `migrations/`.
- Каждая функция сервиса с проверкой прав имеет минимум: тест успеха, тест «нет сессии/прав», тест «чужой объект».
- Табличные тесты для валидаторов.
- Именование: `TestService_Method_Case`.
- Frontend: `vitest` + `@testing-library/react` для компонентов и `lib/*`; `playwright` для 3 e2e-сценариев (см. 09).

## 9. Общие инварианты исполнения

- При изменении миграций, уже применённых в существующей реализации, создавать новую миграцию; исторические файлы не переписывать. В 02 для исправления комментариев добавлена 0013.
- `contracts/locales.json`, `contracts/glossary.json`, минимальный `contracts/messages/en.json` создаются в T01, до всех Go/CLI-потребителей. `make sync-locales` копирует locale-контракт в Go; CI проверяет точное равенство.
- В T02 создать контракт `acl.Subject/Binding/Resource/Perm` без зависимостей от identity; T09 добавляет реализацию ACL. registry использует собственные DTO/снимки полей, а не импортирует сервисы статей, комментариев или identity: эти сервисы вызывают registry, циклов импортов нет.
- `platform/ratelimit.Allow` реализовать атомарным Redis-скриптом INCR+EXPIRE: ключ не остаётся без TTL. Redis-лимиты с окном TTL считаются фиксированными окнами; скользящий интервал применяется только там, где он явно указан отдельно.
- `s3store` расположен в `backend/internal/platform/s3store`; outbox — в `backend/internal/platform/outbox`. Ссылки на сокращённые имена обозначают эти пакеты.
- Проверки `make build/lint/test-web` охватывают web и org. Нет автоматического production-деплоя при исправлении документации.
