# cmd/content-sync — Content Sync Worker (сервис `content-sync`)

Порт `:8081` (`/healthz`, `/status`). Синхронизация S3 → PostgreSQL: словари интерфейса (5 мин) и статьи (1 мин). Команды: `run`, `once ui|articles`, `rollback <version>`, `status`.

## Что здесь будет

- main.go

## Требования

- `docs/tz/06-content-sync.md`

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
