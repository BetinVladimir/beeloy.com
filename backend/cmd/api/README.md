# cmd/api — HTTP API (сервис `api`)

Порт `:8080` (`/api/v1/*`, `/healthz`, `/readyz`), метрики `:9090`. Запускает HTTP-сервер, outbox relay и consumers, фоновые задания. Подкоманды: `api bootstrap-admin --email`, `api outbox status`.

## Что здесь будет

- main.go

## Требования

- `docs/tz/01-conventions.md §6`
- `docs/tz/04-backend-features.md §8, §11`

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
