# Алерты (настраивает человек в выбранной системе мониторинга)

| Условие | Порог |
|---|---|
| Доля 5xx (`http_requests_total{status=~"5.."}`) | > 2 % за 5 мин |
| `GET /readyz` | недоступен > 2 мин |
| Последний успешный прогон Content Sync (`content-sync /healthz`) | > 30 мин |
| `outbox_oldest_unpublished_seconds` | > 300 |
| Длина `events:dead` в Redis | растёт |
| `stream_pending{group="realtime"}` | > 100 устойчиво |
| Диск сервера | > 80 % |
| Свежесть бэкапа | старше 36 ч |
