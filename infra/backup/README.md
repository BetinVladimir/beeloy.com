# infra/backup — резервное копирование

`backup.sh` (pg_dump → restic → S3), `restore-test.sh` (ежемесячная проверка восстановления). RPO 24 ч, RTO 4 ч.

## Требования

- `docs/tz/08-infrastructure.md §7`

Статус: реализовано, покрыто тестами (см. `docs/tz/DONE.md`).
