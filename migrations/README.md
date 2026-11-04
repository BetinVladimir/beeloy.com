# migrations — SQL-миграции

Файлы `NNNN_name.up.sql` / `.down.sql`, 0001…0012. Применяет `cmd/migrate`. Только обратно совместимые изменения; `down` в проде — вручную.

## Что здесь будет

- 0001_identity … 0012_seed (см. 02)

## Требования

- `docs/tz/02-database.md`

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
