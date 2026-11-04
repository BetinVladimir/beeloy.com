# cmd/translator — CLI перевода через Ollama

Запускается оператором вручную (не сервис Compose). Команды: `check`, `article --id`, `ui --source --version [--publish]`. Работает с S3 и Ollama, к PostgreSQL не обращается.

## Что здесь будет

- main.go

## Требования

- `docs/tz/10-translator.md`

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
