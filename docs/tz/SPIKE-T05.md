# Область применимости спайка

Ниже сохранён исторический результат локальной проверки. Это не подтверждение новых требований .org, Google/Facebook callback и исправленной цепочки доверия IP. Повторить проверки Location, нескольких Set-Cookie, state-cookie, SSE и клиентского IP через Caddy; совместимость на настоящем Cloudflare отдельно не проверена этим редактированием.

---

# T05: спайк Cloudflare (результат)

Проверено локально 04.11.2026: сборка `opennextjs-cloudflare build` (Next.js 16.4, OpenNext 1.20) и запуск в `workerd`
через `opennextjs-cloudflare preview`, заглушка API на `127.0.0.1:18080`. Реальный Cloudflare **не** использовался.

| Проверка | Результат |
|---|---|
| SSR страницы с Mantine, `<html lang dir>`, метаданные | работает; словарь из API применяется (`Бийлой` из ответа заглушки) |
| `proxy.ts` (бывш. middleware): `/` → `/{locale}`, cookie `bl_locale` > `Accept-Language` > `bg` | работает (307); OpenNext пишет предупреждение «Node.js middleware experimental» |
| Прокси `/api/v1/**` на `API_ORIGIN`: метод, тело, `Cookie`, `Origin`, `X-Requested-With` | передаются |
| Подмена заголовков клиентом (`X-Proxy-Secret`, `X-Real-Client-IP`, `X-Evil`) | прокси заменяет секрет и IP своими значениями, посторонние заголовки отбрасывает |
| `Set-Cookie` (две cookie) | проходят без потерь |
| Пути вне `/api/v1/` (`/api/metrics`, `/api/v1/../admin`) | 404, на API не уходят |
| SSE `GET /api/v1/events` | **стримится без буферизации** (события приходят с интервалом 0,4 с) |

**Решение:** схема ТЗ-D02 (браузер → `beeloy.com` → прокси Next.js → `api-site.beeloy.com`) принята; запасной вариант
(прямые запросы на `api-site`) не нужен.

**Что остаётся проверить человеку на настоящем Cloudflare** (локально проверить нельзя):
1. Сквозной SSE через Cloudflare → Worker → Cloudflare → Caddy: heartbeat 25 с уже есть в API, ожидаем отсутствие обрывов раньше ~100 с простоя.
2. Реальный IP клиента: Worker берёт `CF-Connecting-IP` и передаёт в `X-Real-Client-IP` (локально заголовка нет).
3. Поведение `proxy.ts` на Workers при росте нагрузки; если Cloudflare/OpenNext перестанет поддерживать Node-proxy, замена — редирект
   локали в `app/[locale]/layout.tsx` + страница-перенаправление для `/` (описано в `docs/tz/07-frontend.md`).
