# apps/org — витрина beeloy.org

Страницы `/{locale}`, `/about`, `/rules`, `/contact`; canonical и sitemap на beeloy.org; кнопки ведут на beeloy.com с локалью в URL.
Нет входа, нет cookie, нет `PROXY_SHARED_SECRET`. Словарь берётся из публичного i18n API (`I18N_API_ORIGIN`), при недоступности — встроенный английский.

```bash
pnpm install && pnpm dev        # http://localhost:3100
pnpm typecheck && pnpm lint && pnpm test && pnpm build
pnpm build:cf                   # сборка Worker; деплой — вручную (infra/CLOUDFLARE.md)
```
Тексты (`org.*` ключи) лежат в `contracts/messages/en.json`; формулировка миссии — рабочий черновик, финальный текст утверждает владелец проекта. Контактный адрес — `CONTACT_EMAIL` (иначе заглушка `TODO-CONTACT@beeloy.org`).
