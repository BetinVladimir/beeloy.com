# apps/web/src/contracts

Типы и таблицы, которые нужны только фронтенду.

## Что здесь будет

- api-types.ts — типы ответов API (зеркало `docs/tz/05-api-contract.md`)
- idea-transitions.ts — таблица допустимых переходов статусов Ideas
- fallback-en.json — аварийный словарь (вход, ошибки, пометка `TODO-LEGAL`)
- i18n-keys.json — список используемых ключей (генерируется `scripts/extract-i18n-keys.ts`)

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
