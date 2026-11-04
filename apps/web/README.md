# apps/web — сообщество beeloy.com

Next.js (App Router) + Mantine + `@opennextjs/cloudflare`. Все пользовательские сценарии: статьи, поиск, Ideas, профили `/people/{slug}`, комментарии, мессенджер, настройки, редактор/модерация/админка, legal.

## Что здесь будет

- package.json, next.config.ts, wrangler.jsonc, tsconfig.json, middleware.ts
- src/app/ — маршруты `[locale]/…` (07 §2) и прокси `api/[...path]/route.ts`
- src/components/ — компоненты (07 §4)
- src/lib/ — api.ts, i18n.ts, locales.ts, seo.ts, returnTo.ts
- src/stores/ — zustand-сторы мессенджера
- src/contracts/ — типы API, таблицы переходов, fallback-словарь
- src/ui/ — тема Mantine
- e2e/ — Playwright-сценарии

## Правила

- Сервис: Cloudflare Worker `beeloy-web`, маршрут `beeloy.com/*`. Запросы `/api/*` проксирует обработчик `src/app/api/[...path]/route.ts` на `https://api-site.beeloy.com` (потоково, SSE) — см. `docs/tz/07-frontend.md`, «Прокси API».
- Тексты интерфейса — только через `t('key')`.

## Требования

- `docs/tz/07-frontend.md`
- `docs/tz/08-infrastructure.md §5`

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
