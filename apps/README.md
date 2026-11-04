# apps — приложения-фронтенды

Два Next.js-приложения, каждое — отдельный Cloudflare Worker со своим `package.json` и lockfile (ТЗ-D01, D03).

- `web/` — beeloy.com, сообщество (вход, статьи, Ideas, мессенджер, модерация).
- `org/` — beeloy.org, публичная витрина (миссия, о проекте, краткие правила, контакты, переход в .com). Без входа и cookie.

Общие данные — в корневом `contracts/` (`make sync-locales` копирует их в приложения и в Go).

Требования: `docs/tz/07-frontend.md`. Статус: `docs/tz/DONE.md`.
