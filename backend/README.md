# backend — Go-модуль `beeloy.com/backend`

Весь серверный код. Один модуль, четыре исполняемых файла.

## Что здесь будет

- cmd/ — точки входа (api, content-sync, migrate, translator)
- internal/ — пакеты
- Dockerfile (multi-stage, distroless, nonroot)
- go.mod, go.sum

## Правила

- Без ORM и без sqlc: SQL только явный и параметризованный.

## Требования

- `docs/tz/01-conventions.md`
- `docs/tz/08-infrastructure.md §4`

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
