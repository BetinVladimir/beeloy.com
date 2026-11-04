# apps/web/src/app — маршруты

Файлы маршрутов App Router. Корень: `layout.tsx`, `page.tsx` (редирект на локаль), `sitemap.ts`, `robots.ts`, `not-found.tsx`, `error.tsx`. Прокси API: `api/[...path]/route.ts` (Worker → `api-site.beeloy.com`). Подкаталог `[locale]/` содержит: `news`, `search`, `ideas`, `people/[slug]`, `r/[id]`, `login`, `settings`, `messages`, `editor`, `mod/articles`, `mod/reports`, `admin/users`, `legal/[doc]`.

## Что здесь будет

- Полный список маршрутов — 07 §2

## Правила

- Приватные страницы: `dynamic = 'force-dynamic'`, `noindex`.
- Публичные страницы не зависят от cookie.

## Требования

- `docs/tz/07-frontend.md §2`

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
