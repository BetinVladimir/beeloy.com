# Cloudflare: что настроить вручную

Всё ниже делает человек в панели Cloudflare и на сервере; в репозитории только конфигурация. Схема: `docs/tz/08-infrastructure.md`.

## 1. DNS
| Имя | Тип | Значение | Proxy |
|---|---|---|---|
| `beeloy.com` | привязка к Worker `beeloy-web` (Workers → Custom Domain или Route `beeloy.com/*`) | — | да |
| `beeloy.org` | привязка к Worker `beeloy-org` (витрина `apps/org`, без авторизации и cookie) | — | да |
| `api-site.beeloy.com` | A | IP сервера Hetzner | **да (оранжевое облако)** |

## 2. SSL/TLS
1. Режим **Full (strict)**.
2. Origin Server → *Create Certificate* для `api-site.beeloy.com`: сохранить на сервер `origin.pem`, `origin.key` в `/etc/beeloy/certs/` (права 600, владелец root; в репозиторий не попадают).
3. SSL/TLS → Origin Server → **Authenticated Origin Pulls** включить; скачать CA Cloudflare и положить как `/etc/beeloy/certs/cf-origin-pull-ca.pem`.

## 3. Cache Rules для `api-site.beeloy.com`
- По умолчанию: **Bypass cache**.
- Исключения «Respect origin»: только `/api/v1/i18n/*` и `/api/v1/categories`. Статьи, поиск, профили, Ideas, комментарии, `/resources`, sitemap-entries и всё с cookie — `private, no-store`: смена видимости должна действовать на следующий же запрос, поэтому их нельзя кэшировать ни на Cloudflare, ни в Next.js.

## 4. Секреты и переменные Worker
```bash
cd apps/web
pnpm exec wrangler secret put PROXY_SHARED_SECRET   # то же значение, что PROXY_SHARED_SECRET в .env сервера
```
Переменные `API_ORIGIN`, `NEXT_PUBLIC_SITE_URL`, `NEXT_PUBLIC_DEFAULT_LOCALE` заданы в `wrangler.jsonc`.

Витрина: `cd apps/org && pnpm exec wrangler deploy` (после `pnpm build:cf`). У Worker `beeloy-org` **нет** `PROXY_SHARED_SECRET`; он читает только публичный словарь с `api-site.beeloy.com`. Контактный адрес — переменная `CONTACT_EMAIL` (до её установки показывается заглушка `TODO-CONTACT@beeloy.org`).

### Google и Facebook
В консолях провайдеров зарегистрировать приложения и callback `https://beeloy.com/api/v1/auth/oauth/google/callback` и `…/facebook/callback`. `GOOGLE_*`/`FACEBOOK_*` — только в `.env` API на сервере (не в Worker). Эндпоинты и scopes в коде (`identity/oauth.go`) соответствуют публичной документации на момент написания и **не проверялись с реальными ключами**; без ключей кнопки ведут на страницу ошибки входа.

## 5. Файрвол Hetzner
Входящий 443 — только диапазоны Cloudflare (`infra/scripts/update-cf-ips.sh`), 22 — только IP администратора. Список Cloudflare идёт в `CLOUDFLARE_CIDRS` (Caddy принимает только такие соседние адреса). `TRUSTED_PROXY_CIDRS` — **не** Cloudflare, а фиксированный адрес Caddy во внутренней сети `proxy` (`172.30.0.2/32`): Go принимает `/api/v1/*` только от него и только с `X-Proxy-Secret` (исключение — публичные GET словарей и категорий).

## 6. Проверка после выкладки
1. `curl -I https://beeloy.com/` → 307 на `/bg`.
2. `curl https://beeloy.com/api/v1/i18n/meta` → JSON (путь: Worker → Cloudflare → Caddy → API).
3. `curl -I https://api-site.beeloy.com/api/v1/i18n/meta` напрямую с чужого IP — работает через Cloudflare, но не при обращении на IP сервера (файрвол/Authenticated Origin Pulls).
4. Войти по e-mail коду, открыть `/messages`, убедиться, что приходят события SSE (heartbeat каждые 25 с не даёт Cloudflare оборвать соединение).
