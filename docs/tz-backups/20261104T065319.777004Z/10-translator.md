# 10. Translator: CLI перевода через Ollama

Основание: «локализация локальными моделями через Ollama», «публикация и перевод пока вручную», «ресурсы со статьями выкладываются уже с переводом» (ТЗ-D11). Поэтому это **консольная утилита, которую запускает оператор**, а не фоновый сервис. Она не входит в `docker-compose` как постоянный сервис и не вызывается API. Код: `backend/cmd/translator`, логика `backend/internal/translator`.

Утилита не обращается к PostgreSQL. Она работает с S3 (чтение/запись, свой ключ) и Ollama (`OLLAMA_URL`, `OLLAMA_MODEL`). Результат подхватывает Content Sync (06).

## 1. Команды

```text
translator check                                    # проверка: Ollama отвечает, модель OLLAMA_MODEL существует (GET /api/tags), S3 доступен
translator article --id <uuid> [--locales de,fr|all] [--force]
translator ui --source <path/to/en.json> --version <v> [--locales ...] [--out <dir>] [--publish]
```
Коды выхода: 0 — всё успешно; 1 — хотя бы одна локаль не переведена; 2 — ошибка запуска (конфиг, недоступен Ollama/S3). Прогресс — строки `slog` (локаль, номер чанка, время), без текста статьи.

## 2. Ollama-клиент (`translator/ollama.go`)

```go
type Client struct{ BaseURL, Model string; HTTP *http.Client /* Timeout = TRANSLATOR_REQUEST_TIMEOUT */ }
type chatReq struct {
    Model    string        `json:"model"`
    Messages []chatMessage `json:"messages"`
    Stream   bool          `json:"stream"`           // false
    Format   string        `json:"format,omitempty"` // "json"
    Options  map[string]any `json:"options"`         // {"temperature":0.2,"num_ctx":8192}
    KeepAlive string       `json:"keep_alive"`       // "10m"
}
type chatMessage struct{ Role, Content string }       // json: role, content
// Chat: POST {BaseURL}/api/chat ; ответ .message.content ; HTTP != 200 или пустой content -> error. Повтор при сетевой ошибке/5xx: 3 попытки, паузы 2s,5s.
func (c *Client) Chat(ctx context.Context, system, user string, jsonMode bool) (string, error)
func (c *Client) Ping(ctx context.Context) error   // GET /api/tags; проверка, что c.Model есть в списке
```

## 3. Языки и промпты

Названия языков для промпта берутся из `contracts/locales.json` (поле `english_name`: «Bulgarian», «Irish», «Maltese», «Ukrainian», …). Утилита читает `locales.json` из файла, путь — флаг `--locales-file` (по умолчанию `contracts/locales.json`).

Системный промпт для статей (подставлять `{src}`, `{dst}`):
```text
You are a professional translator. Translate from {src} to {dst}.
Rules: keep Markdown structure exactly (headings, lists, tables, links, images, emphasis); do not translate code blocks, inline code, URLs, file paths, HTML tags; keep numbers, units and proper names; keep the terms listed in KEEP unchanged; do not add or remove information; do not add comments or explanations.
KEEP: {keep_terms}
Reply with ONE JSON object and nothing else.
```
Пользовательское сообщение для первого вызова статьи: JSON `{"title":"…","summary":"…"}`; ответ: тот же объект с переведёнными значениями. Для тела: `{"body_md":"<чанк>"}`; ответ: `{"body_md":"<перевод чанка>"}`.

Термины `KEEP` читаются из `contracts/glossary.json`: `{"keep":["Beeloy","Beeloy Ideas"]}`.

## 4. Разбиение тела на чанки (`translator/chunk.go`)

```go
// SplitMarkdown делит текст на чанки не длиннее maxRunes (по умолчанию 3000), не разрывая блоки кода (```…```), таблицы и абзацы.
// Алгоритм: разбить по пустым строкам на блоки; блок внутри открытого ``` склеивать до закрывающего; накапливать блоки в чанк, пока не превысит лимит;
// блок больше лимита — резать по строкам. Конкатенация чанков через "\n\n" должна давать исходный текст (с точностью до нормализации пустых строк) — проверяется тестом.
func SplitMarkdown(s string, maxRunes int) []string
```

## 5. Валидация результата (`translator/validate.go`)

Каждая проверка → ошибка → повтор запроса (до 3 попыток на чанк, температура остаётся 0.2; на 3-й попытке в промпт добавляется строка `Your previous answer was invalid: <причина>.`). После 3 неудач локаль считается проваленной; файл **не пишется**.

```go
func ValidateChunk(src, dst string, dstLocale string) error
```
Проверки:
1. JSON парсится, нужные поля — непустые строки.
2. Множество URL (`https?://\S+`) в `dst` равно множеству в `src`.
3. Число строк-ограждений ``` одинаково; тексты внутри ограждённых блоков совпадают побайтово.
4. Множество плейсхолдеров `{name}` совпадает (актуально для UI).
5. Число заголовков Markdown (`^#{1,6} `) одинаково.
6. Отношение длины в рунах `len(dst)/len(src)` ∈ [0.3, 3.5]; для целевых `zh`, `ja`, `ko` — ∈ [0.1, 3.5]; пустой `src` игнорируется.
7. `dst` не равен `src` целиком, если длина > 40 рун и целевой язык отличается от исходного (модель не перевела).

## 6. Режим `article`

1. Читает `articles/{id}/manifest.json` (S3). Статус не важен (можно переводить черновик). `manifest.rev` должен быть ≥ 1.
2. Читает `rev-{manifest.rev}/{source_locale}.json`, проверяет `articles.ValidateLocaleFile` (используется общий код из `internal/articles`).
3. Цели: `--locales` (список или `all` = все из `locales.json`, кроме `source_locale`).
4. Для каждой целевой локали последовательно:
   - Если существует `rev-{rev}/{locale}.json`: при `origin=human` — **пропустить без исключений** (ручной перевод не перезаписывается; `--force` на `human` не действует); при `origin=machine` — пропустить, если нет `--force`.
   - Перевести `title`+`summary` одним вызовом, затем чанки `body_md`; собрать `LocaleFile{origin:"machine", model: OLLAMA_MODEL, generated_at: now}`; `ValidateLocaleFile`; `Put` в S3 с `Content-Type: application/json`.
   - Ошибка локали: лог, `failed++`, перейти к следующей.
5. **Никогда не изменяет `manifest.json`** и файлы других ревизий. Не меняет статус публикации.
6. Повторный запуск продолжает с недостающих локалей (идемпотентно).
7. Итог: таблица «локаль → translated / skipped / failed».

## 7. Режим `ui`

1. Читает исходный словарь (плоский JSON `{"key":"text"}`), проверяет правила ключей и значений (06 §1).
2. Для каждой локали из `locales.json` (кроме `en` или локали исходника): разбивает ключи на пачки по ≤ 40 ключей / ≤ 4000 рун; запрос: `{"key":"text",…}`; системный промпт — вариант промпта §3 с дополнением `Translate only the VALUES; keep every KEY and every {placeholder} unchanged; these are short user-interface strings`. Ответ — объект с **тем же набором ключей**.
3. Проверки: набор ключей идентичен; для каждого значения `ValidateChunk` п.1, 4, 7 (для строк короче 40 рун п.7 не применяется); отношение длины п.6 не проверяется для строк < 12 рун. Ключи с суффиксами `.one/.other/…` переводятся как обычные строки; для языков с иными формами множественного числа клиент использует `.other` (ограничение MVP).
4. Если все локали успешны: пишет `releases/{version}/{locale}.json` (для `en` — копия исходника) в каталог `--out` (по умолчанию `./dist/i18n/{version}`) и формирует `manifest.json` (контракт 06 §1: `schema_version`, `version`, `published_at`, `reference_locale`, `locales[]` с `sha256` и `size`). **Без `--publish`** остановиться — оператор просматривает файлы.
5. С `--publish`: загрузить файлы локалей в S3 (`{S3_PREFIX}i18n/releases/{version}/…`), и **только после успешной загрузки всех** — записать `i18n/manifest.json` (последним). Версия не должна существовать в S3 (иначе ошибка).
6. При провале хотя бы одной локали манифест не создаётся и не публикуется, код выхода 1.

## 8. Тесты (Ollama — `httptest`-заглушка)

- `SplitMarkdown`: блоки кода не разорваны; сумма чанков восстанавливает текст; блок длиннее лимита режется по строкам.
- `ValidateChunk`: потерянный URL; изменённый код-блок; потерянный `{placeholder}`; нормальный CJK-перевод проходит с коэффициентом 0.2; «перевод» равный исходнику отвергается.
- Клиент: повтор при 503, остановка после трёх; пустой ответ → ошибка.
- `article`: все 25 локалей записаны ключами `rev-N/{locale}.json` с `origin=machine`; `manifest.json` не изменён (сравнить байты/ETag); `origin=human` не перезаписан даже с `--force`; повторный запуск пропускает готовые; модель дважды вернула мусор, третий раз корректно → локаль записана; три раза мусор → локаль провалена, код выхода 1, остальные записаны.
- `ui`: набор ключей сохранён во всех локалях; при провале одной локали манифест не создан; без `--publish` в S3 ничего не записано.
- `check`: недоступный Ollama → код 2.
