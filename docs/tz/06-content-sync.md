# 06. Content Sync (S3 JSON → PostgreSQL)

Сервис выполняет **два независимых прогона**: часть A — словари интерфейса (раз в `SYNC_INTERVAL_UI`, 5 мин), часть B — индексация статей (раз в `SYNC_INTERVAL_ARTICLES`, 1 мин). Разные advisory-lock'и (7100 — UI, 7101 — статьи); в `sync_runs.version` пишется `ui:<version>` или `articles:<число обработанных>`.

Отдельный процесс `backend/cmd/content-sync`, логика в `backend/internal/contentsync` (ТЗ-D12). Не Cloudflare Worker.

# Часть A. Словари интерфейса

Назначение: забрать словари интерфейса всех языков (созданные CLI `translator ui` через Ollama, см. 10, или выложенные вручную) из S3, проверить, атомарно активировать версию.

## 1. Контракт данных в S3

Ключи под префиксом `S3_PREFIX` (например `i18n/`):

```text
i18n/manifest.json                 # указатель на последнюю опубликованную версию
i18n/releases/{version}/{locale}.json
```

`manifest.json`:
```json
{
  "schema_version": 1,
  "version": "2026-11-04.1",
  "published_at": "2026-11-04T10:00:00Z",
  "reference_locale": "en",
  "locales": [
    {"code": "en", "file": "releases/2026-11-04.1/en.json", "sha256": "<hex>", "size": 12345}
  ]
}
```
Правила: `version` — строка `^[0-9A-Za-z._-]{1,64}$`, уникальна и не переиспользуется; `locales[].code` — ровно все коды из `contracts/locales.json` (26), без лишних и дубликатов; `file` не содержит `..` и не начинается с `/`; `size` ≤ 1 МиБ; общий размер ≤ 20 МиБ.

Файл локали — плоский JSON-объект `{"ключ": "строка"}`; ключи `^[a-z0-9_]+(\.[a-z0-9_]+)*$`; значения — строки до 5000 символов; допускаются плейсхолдеры `{name}` (`\{[a-zA-Z_][a-zA-Z0-9_]*\}`); формы множественного числа — отдельные ключи с суффиксами `.zero|.one|.two|.few|.many|.other`.

## 2. Go-структуры (`contentsync/model.go`)

```go
type Manifest struct {
    SchemaVersion int            `json:"schema_version"`
    Version       string         `json:"version"`
    PublishedAt   time.Time      `json:"published_at"`
    ReferenceLocale string       `json:"reference_locale"`
    Locales       []ManifestItem `json:"locales"`
}
type ManifestItem struct{ Code, File, SHA256 string; Size int64 }
type Bundle struct{ Locale string; Raw []byte; Messages map[string]string; SHA256 string }
type Result string // "ok" | "noop" | "failed"
```

## 3. Компоненты

```go
// contentsync/s3.go
type Source interface {
    GetManifest(ctx context.Context) (Manifest, []byte, error)
    GetFile(ctx context.Context, relPath string, maxSize int64) ([]byte, error)  // io.LimitReader(maxSize+1) -> error if exceeded
}
type S3Source struct{ client *s3.Client; bucket, prefix string }  // aws-sdk-go-v2, custom endpoint, path-style addressing enabled via config

// contentsync/validate.go
func ValidateManifest(m Manifest, requiredLocales []string) error
func ValidateBundle(item ManifestItem, raw []byte) (Bundle, error)   // size == item.Size, sha256(raw)==item.SHA256 (hex, lower), valid JSON object, key/value rules
// ValidateSet: all bundles must have the same key set as default locale bundle; for each key placeholders set equals default's placeholders set.
// Missing keys in non-default locale => error (strict). Returns list of ALL problems (not only first), max 50 listed.
func ValidateSet(m Manifest, bundles map[string]Bundle) []string

// contentsync/store.go (SQL only)
type Store struct{ pool *pgxpool.Pool }
func (s *Store) GetRelease(ctx, version string) (status string, found bool, err error)
func (s *Store) CurrentActive(ctx) (version string, found bool, err error)
func (s *Store) StageRelease(ctx, m Manifest, raw []byte, bundles map[string]Bundle) error   // one tx: INSERT release(status=staged) + all bundles
func (s *Store) Activate(ctx, version string) error   // one tx: UPDATE previous active -> superseded; UPDATE release -> active, activated_at=now(); INSERT ... ON CONFLICT (id) DO UPDATE i18n_active; audit i18n.activate (actor NULL)
func (s *Store) MarkFailed(ctx, version, errText string) error
func (s *Store) PruneOld(ctx, keep int) error         // keep latest `keep`=5 releases (never the active one); delete others (cascade bundles)
func (s *Store) StartRun(ctx) (uuid.UUID, error) ; FinishRun(ctx, id, result Result, version, errText string) error
func (s *Store) TryLock(ctx) (release func(), ok bool, err error)  // pg_try_advisory_lock(7100) on a dedicated connection

// contentsync/syncer.go
type Syncer struct{ src Source; store *Store; rdb *redis.Client; required []string; log *slog.Logger }
func (s *Syncer) RunOnce(ctx context.Context) (Result, string /*version*/, error)
func (s *Syncer) Loop(ctx context.Context, interval time.Duration)
func (s *Syncer) Rollback(ctx context.Context, version string) error
```

## 4. Алгоритм `RunOnce` (выполнить буквально)

1. `ok := store.TryLock()`; если занято → return `noop` (другая реплика работает).
2. `runID := StartRun`. Контекст с таймаутом 2 минуты на весь прогон.
3. `m, rawManifest := src.GetManifest()` (до 3 попыток, пауза 1 с, 2 с, 4 с; только для сетевых ошибок). Ошибка → `FinishRun(failed)`; **активная версия не меняется**.
4. `ValidateManifest`. Ошибка → failed.
5. `active := store.CurrentActive()`. Если `m.Version == active` → `FinishRun(noop)`, return.
6. Если релиз `m.Version` уже существует в БД со статусом `failed` или `staged` → удалить его (`DELETE FROM i18n_releases WHERE version=$1 AND status IN ('failed','staged')`) и продолжить; со статусом `active/superseded` → return `noop` (версии не переиспользуются, лог warn).
7. Параллельно (не более 6 горутин, `errgroup`) скачать все 26 файлов `GetFile(item.File, item.Size)`; `ValidateBundle` для каждого.
8. `problems := ValidateSet`. Если непусто → `StageRelease` не вызывать; записать `i18n_releases(version, status='failed', error=join(problems))` через `MarkFailed` (создать запись-заглушку), `FinishRun(failed)`, return. Активная версия не меняется.
9. `StageRelease` (транзакция), затем `Activate` (транзакция). Ошибка Activate → `MarkFailed`, failed.
10. После успеха: `rdb.Del` ключей `i18n:*` через `SCAN MATCH i18n:*` (Redis недоступен → warn, кэш и так версионный); `PruneOld(5)`; `FinishRun(ok, version)`.
11. Освободить lock. Логировать итог: `version`, `duration`, `result`; без содержимого словарей.

Повторный запуск с той же версией безопасен (шаг 5/6). Частичной публикации нет: читатели видят либо старую активную версию, либо новую целиком (переключение — одно обновление `i18n_active`).

# Часть B. Индексация статей

Назначение: зарегистрировать в БД статьи и их версии из S3 (формат — 04 §1.1): таблицы `articles`, `article_revisions`, и — для текущей утверждённой ревизии — живой индекс `article_locales`. **Sync ничего не публикует**: он не читает статус из S3 и никогда не меняет `status`, `visibility`, `published_rev` (новая статья получает `in_review`). Решение о публикации и аудитории принимает модератор (04 §1.2).

## B.1 Компоненты (`contentsync/articles.go`)

```go
type ArticleSyncer struct{ src s3store.ObjectStore; pool *pgxpool.Pool; repo *articles.Repo; slugs *identity.SlugService; isLocale func(string) bool; log *slog.Logger }  // тот же пакет articles, функции Upsert*/RefreshLive
type objInfo struct{ Key, ETag string; Size int64 }
func (a *ArticleSyncer) RunOnce(ctx context.Context) (processed, failed int, err error)
func (a *ArticleSyncer) Loop(ctx context.Context, interval time.Duration)
func fingerprint(objs []objInfo) string   // sha256 hex от отсортированных строк "key|etag"
```

## B.2 Алгоритм `RunOnce`

1. `TryLock(7101)`; занято → return (0,0,nil).
2. `List(prefix="articles/")` постранично (1000 ключей), сгруппировать объекты по `article_id` (второй сегмент ключа; ключи с неверным UUID пропускать с warn).
3. Для каждого `article_id` последовательно (ошибка одной статьи не останавливает остальные: `failed++`, warn с id и причиной):
   1. Должен существовать `manifest.json`, иначе пропустить (warn).
   2. `fp := fingerprint(объекты статьи)`; сравнить с `articles.s3_fingerprint` (нет строки → пусто). Равны → пропустить.
   3. `GetObject(manifest)` (≤ 64 КиБ) → `ValidateManifest`. Категория по `category_key` должна существовать, иначе ошибка статьи. Существующему article manifest.rev не может уменьшать articles.rev; sync не перезаписывает более новый manifest старой версией. author_id неизвестного/deleted пользователя не восстанавливается в публичную авторскую связь.
   4. Перечислить все файлы `rev-{n}/{locale}.json` с `n ≤ manifest.rev` (локаль не из `locales.json` → игнорировать, warn). При изменившемся fingerprint статьи скачать и проверить sha256 всех известных `(article_id,rev,locale)` (per-file ETag не хранится): несовпадение с реестром — нарушение неизменяемости, ошибка статьи; хеш не заменять. Для отсутствующих строк скачать файл. `--force` не может менять опубликованный или неопубликованный файл по прежнему ключу.
   5. Скачать новые файлы (≤ 512 КиБ, параллельно ≤ 6), `ValidateLocaleFile`. Невалидный файл или ошибка загрузки означает failed для этой статьи: не обновлять fingerprint и не активировать частично изменённый индекс. Другие статьи продолжают; следующий прогон повторяет неуспешную статью даже без изменения S3.
   6. Одна транзакция: если slug манифеста не закреплён за этой статьёй — `slugs.Assign` (занят другой сущностью → ошибка статьи, откат); `UpsertFromManifest` (новая → `in_review`; существующая → статус/видимость/`published_rev` **не меняются**); `UpsertRevision` для каждого валидного файла (`sha256`, `size` — по скачанным байтам; `searchText` из файла; существующие не перезаписываются); если `articles.published_rev > 0` → `RefreshLive(article)` (так подхватываются переводы ровно published_rev; более старые переводы в live не включаются; новая исходная ревизия остаётся неопубликованной); `UPDATE articles SET s3_fingerprint=$fp, synced_at=now()`.
   7. `processed++`.
4. Статьи, которые есть в БД, но манифест которых исчез из S3, **не удаляются** (warn `manifest missing`).
5. `FinishRun`: result `ok` при `failed==0`, иначе `failed`.

Повторный запуск без изменений в S3 ничего не пишет (п.3.2). Состояние после публикации через API и после sync одинаково (write-through и sync используют одни и те же `Upsert*`).

## B.3 Тесты части B

- Манифест + 3 файла локалей → 1 статья со статусом `in_review`, 3 строки `article_revisions`, **0 строк `article_locales`** (ничего не опубликовано); поиск не находит её; после `Publish` (через сервис) поиск находит слово из тела.
- Статья `published` с `published_rev=1`: появление `rev-2/*` и нового файла перевода `rev-1/de.json` → `de` попал в `article_locales`, текст `rev-2` — нет; `status` и `published_rev` не изменены.
- Повторный sync → ноль записей (fingerprint).
- Невалидный файл одной локали не мешает другим статьям, но fingerprint этой статьи не сохраняется; после восстановления чтения файл обрабатывается повторно без изменения S3.
- Конфликт slug с другой сущностью → статья пропущена, остальные обработаны.
- Файл с `rev` выше `manifest.rev` игнорируется.
- Файл удалён из S3 → строки реестра `article_revisions` не удаляются (warn).
- Две реплики одновременно → обрабатывает одна.
- Статья, созданная через API, и статья, выложенная только в S3, после sync выглядят одинаково.

# Общее для обеих частей

## CLI (`cmd/content-sync`)

```text
content-sync run                  # цикл: UI (SYNC_INTERVAL_UI) и статьи (SYNC_INTERVAL_ARTICLES) в двух горутинах; первый прогон сразу
content-sync once ui|articles     # один прогон; код выхода 0 при ok/noop, 1 при failed
content-sync rollback <version>   # (UI) активировать ранее загруженную версию (superseded/active), без обращения к S3
content-sync status               # активная версия UI и последние 5 sync_runs
```
Корректное завершение: по SIGTERM контексты отменяются, текущий прогон доводится до конца транзакции или откатывается; код выхода 0.
Здоровье: HTTP `:8081/healthz` (200, если оба прогона успешно завершались не позже `3×interval` назад или процесс живёт < 5 мин) и `/status` (JSON последних `sync_runs`).

## Права и секреты

- Доступ content-sync к S3 — отдельный ключ **только на чтение** (`GetObject`, `ListBucket`). Запись в S3 делают API (статьи), CLI `translator` и оператор со своими ключами.
- В S3 лежат словари интерфейса и статьи (включая черновики); персональных данных пользователей (email, сообщения) там быть не должно, `author_id` — псевдонимный UUID, всё ещё требующий учёта в политике данных; его нельзя автоматически считать анонимными данными. Бакет приватный: клиенты читают контент только через API.
- Переменные — 01 §3; логи не содержат ключей доступа.

## Тесты части A

- Юнит: `ValidateManifest` (дубликаты локалей, лишняя локаль, `..` в пути, слишком большой size), `ValidateBundle` (неверный sha256, массив вместо объекта, нестроковые значения), `ValidateSet` (пропущенный ключ, разные плейсхолдеры).
- Интеграция (testcontainers Postgres + MinIO): успешная синхронизация → `i18n_active` указывает на версию; повторный запуск → `noop`; битый файл одной локали → активная версия **не изменилась**, `failed` записан; параллельный запуск двух `RunOnce` → один `ok`, второй `noop`; rollback; `PruneOld` не удаляет активную.

## Кэш, rollback и обязательные уточнения

Атомарность UI-релиза гарантируется на уровне i18n_active в БД; отдельные HTTP/Next.js кэши могут временно видеть прежний словарь в пределах TTL. Это допустимо для UI, но не распространяется на ACL контента. Rollback активирует сохранённую версию; следующий polling не должен самовольно отменять ручной rollback: оператор сначала возвращает S3 i18n/manifest.json к выбранной версии либо явно освобождает режим rollback перед возобновлением автоматического sync. Команда rollback без такого подтверждённого порядка возвращает conflict и инструкцию, а не обещает постоянный откат.

Существующий `article_revisions.sha256` никогда не заменяется вслед за изменённым S3-файлом. Нарушение целостности — failed с уведомлением оператору; прежний live остаётся, но повреждённый объект не выдаётся. Источник/перевод исправляется новой ревизией и новым модераторским утверждением. Тесты: изменённый machine-файл отвергнут; доступ к старой корректной версии после восстановления бэкапа проверен; fingerprint не блокирует повторы временно неудавшейся загрузки.
