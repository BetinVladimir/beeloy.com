# contracts — общие данные

Не JS-пакет, а обычные файлы, которые читают сразу несколько частей проекта: фронтенд, Go-код и CLI переводчика.

## Что здесь будет

- locales.json — 26 языков (`default: bg`, `fallback: en`, `source: en`, поля `code`, `name`, `english_name`, `dir`)
- glossary.json — термины, которые не переводятся
- messages/en.json — исходный словарь интерфейса (остальные 25 языков создаёт CLI `translator ui` и публикует в S3)

## Правила

- Копия `locales.json` для Go создаётся командой `make sync-locales`; CI проверяет, что копия совпадает.

## Требования

- `docs/tz/07-frontend.md §2.2`
- `docs/tz/10-translator.md`

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
