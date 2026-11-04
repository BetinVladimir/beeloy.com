# infra/compose — Docker Compose

`docker-compose.yml` (caddy, api, content-sync, migrate, postgres, redis, backup), `compose.dev.yml` (+ mailpit, minio, порты на 127.0.0.1), `.env.example` со всеми ключами.

## Правила

- `translator` в Compose не входит.

## Требования

- `docs/tz/08-infrastructure.md §2, §9`

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
