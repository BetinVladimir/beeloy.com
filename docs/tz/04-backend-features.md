# 04. Backend: функциональные пакеты

Обозначения: «Require(P)» = `authz.Require(&subject, P, resource)`; ошибки — из `apperr` (01 §4). Каждая мутация, помеченная **[audit]**, пишет `audit.Record` в той же транзакции. Любая операция «получить объект, на который нет права» возвращает `not_found`, а не `forbidden`, если объект приватный (диалоги, чужие черновики).

## 1. articles (тексты — в S3; состояние, версии и индекс — в БД; публикует модератор)

### 1.1 Формат в S3 (источник истины текстов; префикс `S3_PREFIX`)

```text
articles/{article_id}/manifest.json            # описание статьи БЕЗ статуса публикации
articles/{article_id}/rev-{rev}/{locale}.json  # один файл на язык и ревизию исходного текста
```
`manifest.json` (статус, видимость и «опубликовано» в нём **отсутствуют** — их задаёт только БД):
```json
{"schema_version":1,"article_id":"<uuid>","slug":"my-article","category_key":"news",
 "source_locale":"bg","rev":3,"cover_url":null,"author_id":"<uuid|null>","created_at":"RFC3339","updated_at":"RFC3339"}
```
`rev-{rev}/{locale}.json`:
```json
{"schema_version":1,"article_id":"<uuid>","locale":"de","rev":3,"origin":"human|machine","model":null,
 "title":"…","summary":"…","body_md":"…","generated_at":"RFC3339"}
```
Правила: файлы ревизий **неизменяемы** (перезапись запрещена для обоих origin: только атомарный PutIfAbsent; существующий ключ → conflict. Exists+Put не обеспечивает неизменяемость при гонке. `--force` отвергается без записи); новая версия исходного текста = новая `rev`. Размер файла ≤ 512 КиБ. «Ресурс с готовыми переводами» выкладывается записью `manifest.json` и файлов `rev-N/{locale}.json` (`origin: "human"`) в S3; Content Sync (06 часть B) регистрирует их в БД, **ничего не публикуя**. `author_id` в манифесте — псевдоним-UUID.

```go
// articles/s3layout.go
func ManifestKey(id uuid.UUID) string                         // "articles/{id}/manifest.json"
func LocaleKey(id uuid.UUID, rev int, locale string) string   // "articles/{id}/rev-{rev}/{locale}.json"
type Manifest struct { SchemaVersion int; ArticleID uuid.UUID; Slug, CategoryKey, SourceLocale string; Rev int; CoverURL *string; AuthorID *uuid.UUID; CreatedAt, UpdatedAt time.Time } // json по схеме выше
type LocaleFile struct { SchemaVersion int; ArticleID uuid.UUID; Locale string; Rev int; Origin string; Model *string; Title, Summary, BodyMD string; GeneratedAt time.Time }
func ValidateManifest(m Manifest, keyID uuid.UUID, knownLocales func(string) bool) error      // schema_version==1; article_id==keyID; slug valid(article); source_locale известна; rev>=0
func ValidateLocaleFile(f LocaleFile, raw []byte, wantID uuid.UUID, wantLocale string, wantRev int) error // id/locale/rev совпадают с путём; origin ∈ {human,machine}; title 3–200; summary 10–400 (руны); body_md 1–100000; len(raw) ≤ 512 KiB
func SearchText(f LocaleFile) string                          // title + " " + summary + " " + body_md (идёт в to_tsvector, в БД не сохраняется)
```

### 1.2 Жизненный цикл и правила публикации

```text
(API) Create ──► draft ──Submit(editor)──► in_review ──Publish(moderator, rev, visibility)──► published ──Unpublish──► archived
(S3 upload + sync) ───────────────────────► in_review (создаётся синхронизацией, не публикуется)
published + новая rev (SaveSource или загрузка) ─► остаётся published с прежним published_rev; в очереди модерации появляется «ожидает утверждения новой версии»
```
- Публикует и определяет видимость **только** `moderator`/`admin` (`PermArticlePublish`). Editor пишет и отправляет на проверку (`PermArticleWrite`).
- Читатели всегда видят ревизию `published_rev` (по каждой локали — файл **ровно** `rev = published_rev`). Переводы, появившиеся позже для уже утверждённой ревизии, включаются автоматически. Переводы старых ревизий в публичный индекс не попадают: при отсутствии перевода используется en/source этой же утверждённой ревизии с fallback/noindex, а не устаревший текст. Новая исходная ревизия невидима до нового утверждения.
- `visibility`: `public` (в списках, поиске, sitemap) или `restricted` (только назначенные пользователи из `article_access` и staff с `article.write`/`article.publish`; не попадает в списки, поиск, sitemap; ответы `Cache-Control: private, no-store`, страница `noindex`).
- Content Sync **никогда** не меняет `status`, `visibility`, `published_rev`; для новой статьи ставит `in_review`.

### 1.3 Модель (Go)
```go
type Category struct{ ID uuid.UUID; Key string; Names map[string]string; SortOrder int }
type ArticleStatus string // draft | in_review | published | archived
type Visibility string     // public | restricted
type Article struct {
    ID, CategoryID uuid.UUID; AuthorID *uuid.UUID; SourceLocale string; Status ArticleStatus; Visibility Visibility
    Rev, PublishedRev int; CoverURL *string; PublishedAt, ReviewedAt *time.Time; ReviewedBy *uuid.UUID
    CreatedAt, UpdatedAt time.Time; Slug string
}
type RevisionRow struct { ArticleID uuid.UUID; Rev int; Locale, Origin string; Model *string; Title, Summary, SHA256 string; Size int; CreatedAt time.Time }
type LiveRow struct { ArticleID uuid.UUID; Locale string; Rev int; Origin string; Model *string; Title, Summary, SHA256 string; Size int }
type ArticleListItem struct { ID uuid.UUID; Slug, CategoryKey, Locale, Title, Summary string; CoverURL *string; PublishedAt time.Time; FallbackFrom *string }
type ArticleView struct {
    ID uuid.UUID; Slug, CategoryKey, Locale, Title, Summary, BodyMD string; Visibility Visibility
    CoverURL *string; PublishedAt time.Time; AuthorName, AuthorSlug string
    Origin string; AvailableLocales []string; FallbackFrom *string
}
```

### 1.4 Repo (только БД)
```go
ListCategories(ctx, q) ([]Category, error); GetCategoryByKey(ctx, q, key) (Category, error); GetCategoryByID(ctx, q, id) (Category, error)
InsertArticle(ctx, q, a Article) error ; GetArticle(ctx, q, id) (Article, error)
ReserveRev(ctx, q, id) (newRev int, err error)                      // UPDATE articles SET rev=rev+1, updated_at=now() WHERE id=$1 RETURNING rev
UpdateMeta(ctx, q, id, categoryID *uuid.UUID, coverURL **string) error
SetStatus(ctx, q, id, from []ArticleStatus, to ArticleStatus) (bool, error)     // условный переход: WHERE status = ANY($from)
Approve(ctx, q, id, rev int, vis Visibility, reviewer uuid.UUID) error           // published_rev=rev, status='published', visibility, reviewed_*, published_at=COALESCE(published_at, now())
SetAccess(ctx, q, id, userIDs []uuid.UUID) error                                 // replace article_access
HasAccess(ctx, q, id, userID) (bool, error)
UpsertRevision(ctx, q, r RevisionRow, searchText string) error                   // ON CONFLICT DO NOTHING (ревизии неизменяемы); возвращает inserted
ListRevisions(ctx, q, articleID) ([]RevisionRow, error)
RefreshLive(ctx, q, articleID uuid.UUID) error
   // в одной транзакции: live := SELECT * FROM article_revisions WHERE article_id=$1 AND rev = (SELECT published_rev FROM articles WHERE id=$1);
   // UPSERT всех live (копируя search_vector) в article_locales; DELETE FROM article_locales WHERE article_id=$1 AND locale NOT IN (live locales). published_rev=0 -> удалить все.
ListPublished(ctx, q, locale string, categoryKey *string, cur Cursor, limit int) ([]ArticleListItem, error)  // status='published' AND visibility='public'; JOIN LATERAL по приоритету локали: requested → fallback('en') → source_locale
ListAccessibleRestricted(ctx, q, userID, cur, limit, locale) ([]ArticleListItem, error)   // restricted, доступные пользователю
GetPublishedByID(ctx, q, id) (Article, error)                                      // status='published'
PickLive(ctx, q, articleID, requested string) (LiveRow, fellBack bool, err error)  // requested → 'en' → source_locale
ListForEditor(ctx, q, categoryIDs *[]uuid.UUID, cur Cursor, limit int) ([]EditorListItem, error)
ListReviewQueue(ctx, q, cur Cursor, limit int) ([]ReviewItem, error)               // status='in_review' OR (status='published' AND rev>published_rev)
SearchLive(ctx, q, locale, query string, useTrigram bool, offset, limit int) ([]SearchHit, error)  // только public+published
SitemapEntries(ctx, q, cur Cursor, limit int) ([]SitemapEntry, error)             // только public+published
UpsertFromManifest(ctx, q, m Manifest, categoryID uuid.UUID) (created bool, err error)  // новая → status='in_review'; существующая → меняет ТОЛЬКО category_id, cover_url, source_locale, rev=GREATEST(rev,m.rev), updated_at; НЕ трогает status/visibility/published_rev
```

### 1.5 Service
```go
type Service struct{ repo *Repo; pool *pgxpool.Pool; store s3store.ObjectStore; slugs *identity.SlugService; authz acl.Authorizer; rdb *redis.Client; audit *audit.Service }

// Create: Require(PermArticleWrite, Resource{CategoryID}). Валидация: category существует, source_locale ∈ locales.json, slug (если задан) Validate+IsAvailable иначе GenerateUnique(article, TitleForSlug).
// Порядок: id=New(); (1) S3 Put manifest (rev=0); (2) tx{InsertArticle(status=draft); slugs.Assign}. Осиротевший manifest при сбое (2) подхватит sync (станет in_review, не публичный).
func (s *Service) Create(ctx, subj acl.Subject, req CreateArticleReq) (Article, error)
func (s *Service) UpdateMeta(ctx, subj, id, req UpdateArticleReq) error      // Require(PermArticleWrite) для текущей и новой категории; S3 manifest CAS, затем БД; при частичном сбое sync регистрирует согласованные метаданные

// SaveSource: Require(PermArticleWrite). 1) валидация; 2) rev := ReserveRev (БД) — защита от гонки; 3) S3 PutIfAbsent rev-{rev}/{source_locale}.json (origin=human; существующий ключ — conflict), затем CAS-обновление manifest (GetWithETag + PutIfMatch, до 3 повторов при конфликте; rev := max(manifest.rev, reservedRev), никогда не уменьшать rev);
// 4) tx{ UpsertRevision(source row); registry.Upsert; outbox.Add }. При ошибке S3 резерв rev может оставить пропуск; он не публикуется без исходного файла. Sync восстанавливает успешно записанные файлы. Метаданные манифеста UpdateMeta также меняются через CAS; при конфликте заново проверяются права на категорию. Статус не меняется; archived остаётся archived.
func (s *Service) SaveSource(ctx, subj, id, req SourceReq) (int, error)

// PutTranslation (ручной перевод): Require(PermArticleWrite). rev берётся из запроса (по умолчанию article.Rev), 1 ≤ rev ≤ article.Rev; locale ∈ locales.json, ≠ source_locale; в БД должна быть ревизия источника этого rev (иначе conflict "no_source_text").
// S3 PutIfAbsent rev-{rev}/{locale}.json (origin=human; любой существующий файл, в том числе machine, → conflict "exists"), UpsertRevision; если rev == PublishedRev (и PublishedRev>0) → RefreshLive. Исправление перевода требует новой source rev (можно скопировать исходный текст без изменений), новых переводов и нового утверждения.
func (s *Service) PutTranslation(ctx, subj, id uuid.UUID, rev int, locale string, req TranslationReq) error

// Submit (draft → in_review): Require(PermArticleWrite); требует Rev>=1 и ревизию источника, иначе conflict("no_source_text").
func (s *Service) Submit(ctx, subj, id) error
// Publish (утверждение модератором): Require(PermArticlePublish, category). Параметры: rev (1..article.Rev; в article_revisions должна быть строка source_locale для этого rev), visibility, viewerIDs (только для restricted; пользователи должны существовать).
// tx{ Approve; SetAccess(restricted ? viewerIDs : none); RefreshLive; audit ActionArticlePublish(meta{rev,visibility}) }. Инвалидировать Redis-ключи "art:*" не нужно (ключ содержит sha256).
func (s *Service) Publish(ctx, subj, id uuid.UUID, rev int, vis Visibility, viewerIDs []uuid.UUID) error
// Unpublish: Require(PermArticlePublish). status 'published' → 'archived'; [audit]. Reject (in_review → draft): Require(PermArticlePublish), необязательная note в audit.
func (s *Service) Unpublish(ctx, subj, id) error ; func (s *Service) Reject(ctx, subj, id, note string) error

// Публичные чтения:
func (s *Service) List(ctx, locale string, categoryKey *string, cur Cursor, limit int) ([]ArticleListItem, string, error)
// GetBySlug(viewer может быть nil): Resolve slug -> id, canonical; GetPublishedByID (иначе not_found); текущее состояние публикации проверяется на каждом запросе без общего HTTP-кэша; если visibility=restricted — нужен viewer с HasAccess или Can(PermArticleWrite|PermArticlePublish), иначе not_found;
// PickLive; тело: Redis "art:{sha256}" -> hit; miss: store.Get(LocaleKey(id,row.Rev,row.Locale)), проверка sha256(bytes)==row.SHA256 (иначе internal + log "s3 integrity"), положить в Redis TTL 1h (Redis недоступен — без кэша);
// ArticleView (AuthorName/AuthorSlug из users — всегда nickname и slug; удалённый автор → "Deleted user", пустой slug).
func (s *Service) GetBySlug(ctx, viewer *acl.Subject, slug, locale string) (ArticleView, string, error)
func (s *Service) ListMyRestricted(ctx, subj, locale string, cur Cursor, limit int) ([]ArticleListItem, string, error)
// Editor / moderation:
func (s *Service) ListForEditor(ctx, subj, cur Cursor, limit int) ([]EditorListItem, string, error)   // Require(PermArticleWrite); category-editor видит только свои категории
func (s *Service) ReviewQueue(ctx, subj, cur Cursor, limit int) ([]ReviewItem, string, error)         // Require(PermArticlePublish)
// GetForEditor: Require(PermArticleWrite) ИЛИ PermArticlePublish. Возвращает Article, []RevisionRow (по rev и locale, с флагом stale = row.Rev < article.Rev), исходный текст последней ревизии (из S3), список viewer_ids.
func (s *Service) GetForEditor(ctx, subj, id) (EditorArticle, error)
// GetRevisionText: тот же доступ; тело произвольной ревизии/локали из S3 для предпросмотра модератором (проверка sha256 как выше). 
func (s *Service) GetRevisionText(ctx, subj, id uuid.UUID, rev int, locale string) (LocaleFile, error)
```

### 1.6 Поиск: интерфейс `SearchProvider` (`internal/search`)
```go
// Позволяет позднее заменить PostgreSQL на другой движок без смены API.
type Provider interface {
    SearchArticles(ctx context.Context, q ArticleQuery) ([]ArticleHit, int /*nextOffset or -1*/, error)
    SearchPeople(ctx context.Context, q PeopleQuery) ([]PersonHit, int, error)
}
type ArticleQuery struct{ Text, Locale string; Offset, Limit int }
type PeopleQuery  struct{ Text string; Offset, Limit int }
type PersonHit struct{ UserID uuid.UUID; Nickname, Slug string; AvatarURL *string }
type PGProvider struct{ pool *pgxpool.Pool }  // единственная реализация MVP
```
Правила `q`: после `TrimSpace` 2–100 рун, иначе `validation_failed{q}`.
- **Статьи** (`PGProvider.SearchArticles`): только `status='published' AND visibility='public'` и строки `article_locales` запрошенной локали. FTS: `al.search_vector @@ websearch_to_tsquery('simple',$2) ORDER BY ts_rank(...) DESC, a.published_at DESC, a.id DESC LIMIT $3 OFFSET $4`. Триграммы используются, если запрос содержит символы CJK/хангыль/кана ИЛИ FTS вернул 0 строк: `(al.title||' '||al.summary) ILIKE '%'||$2||'%'` (экранировать `%`, `_`, `\`), сортировка `similarity(al.title,$2) DESC`.
- **Люди** (`PGProvider.SearchPeople`): только пользователи `status='active' AND profile_public=true` (пользователь с `profile_public=false` скрыт из поиска людей, но его страница доступна по прямой ссылке). Частичное совпадение по `slug` и `nickname`: `slug ILIKE $2||'%' OR nickname ILIKE '%'||$2||'%'` (экранирование как выше; `@` в начале запроса отбрасывается); порядок: точное совпадение slug, затем префикс slug, затем `similarity(nickname,$2) DESC`, затем `nickname`. Индексы — миграция 0010 (trgm).
- Пагинация — offset-курсор (`base64url(offset)`, максимум offset 200), `limit` ≤ 20. Закрытые данные (email, сообщения, restricted-статьи, черновики) в результатах и подсказках отсутствуют.

## 2. ideas

### Модель
```go
type Status string
const (Submitted Status = "submitted"; UnderReview = "under_review"; NeedsInfo = "needs_info"; Planned = "planned"; InProgress = "in_progress"; Delivered = "delivered"; Declined = "declined"; Duplicate = "duplicate")
type Idea struct {
    ID uuid.UUID; Slug string; AuthorID *uuid.UUID; AuthorName, AuthorSlug string
    Title, Body, Category, Lang string; Status Status; TeamReply *string; TeamReplyAt *time.Time
    DuplicateOfSlug *string; VotesCount int; CreatedAt, UpdatedAt time.Time
    ViewerVoted bool // filled only if viewer authenticated
}
type HistoryItem struct{ From *Status; To Status; Note string; At time.Time }
```

### Таблица переходов (константа `transitions map[Status][]Status`)
```
submitted   -> under_review, needs_info, declined, duplicate
needs_info  -> under_review, declined
under_review-> planned, needs_info, declined, duplicate
planned     -> in_progress, declined
in_progress -> delivered, planned
delivered, declined, duplicate -> (нет переходов)
```
Правила: `declined` и `duplicate` требуют `note` (≥10 рун); `duplicate` требует `duplicate_of` (существующая идея, ≠ самой себя, не в статусе `duplicate`). Остальные `note` необязательна (≤500).

### Repo
```go
Insert(ctx, q, i Idea) error
GetByID(ctx, q, id, viewer *uuid.UUID) (Idea, error)
GetBySlug(...) // через slugs.Resolve, затем GetByID
List(ctx, q, f ListFilter{Status *Status; Category *string; Sort "new"|"top"; AuthorID *uuid.UUID}, cur Cursor, limit int, viewer *uuid.UUID) ([]Idea, error)
// sort=top: ORDER BY votes_count DESC, created_at DESC, id DESC ; cursor.V = votes_count
UpdateContent(ctx, q, id, title, body, category string) error
UpdateStatus(ctx, q, id, to Status, duplicateOf *uuid.UUID) error      // WHERE id=$1 AND status=$expectedFrom  (оптимистичная проверка); 0 rows -> conflict
InsertHistory(ctx, q, id, from *Status, to Status, actor *uuid.UUID, note string) error
ListHistory(ctx, q, ideaID) ([]HistoryItem, error)
SetReply(ctx, q, id, text string, by uuid.UUID) error
InsertVote(ctx, q, ideaID, userID) (inserted bool, err error)          // ON CONFLICT DO NOTHING
DeleteVote(ctx, q, ideaID, userID) (deleted bool, err error)
AddVotesCount(ctx, q, ideaID, delta int) error
CountCreatedSince(ctx, q, userID, since time.Time) (int, error)
```

### Service
```go
// Create: Require(PermIdeaCreate). Limit: CountCreatedSince(24h) >= 5 -> rate_limited (HTTP 429). Also Redis rule idea_create 5/24h by user as fast path.
// Validate: title 5-120, body 20-5000, category ∈ {product,community,content,other}, lang = request locale ∈ locales.json.
// tx{ Insert(status=submitted); slug=GenerateUnique(idea,title); Assign; InsertHistory(nil→submitted) }.
func (s *Service) Create(ctx, subj, req CreateIdeaReq) (Idea, error)
// Edit by author: only if idea.AuthorID==subj.UserID AND status ∈ {submitted, needs_info}; otherwise forbidden/conflict("not_editable").
// If status was needs_info -> transition to under_review automatically + history note "edited by author". Not the owner -> forbidden (the idea is public, so not_found would be misleading).
func (s *Service) Edit(ctx, subj, id, req EditIdeaReq) (Idea, error)
// SetStatus: Require(PermIdeaSetStatus). Validate transition + note + duplicate_of. tx{ UpdateStatus(expectedFrom); InsertHistory; audit(ActionIdeaStatus) }.
func (s *Service) SetStatus(ctx, subj, id, to Status, note string, duplicateOf *uuid.UUID) error
// Reply: Require(PermIdeaReply). Text 1-2000. [audit]
func (s *Service) Reply(ctx, subj, id, text string) error
// Vote: Require auth (member). Cannot vote for own idea -> conflict("own_idea"); cannot vote on terminal statuses (delivered/declined/duplicate) -> conflict("closed").
// tx{ InsertVote; if inserted AddVotesCount(+1) }  — idempotent: repeated PUT returns 204 without changing the count.
func (s *Service) Vote(ctx, subj, ideaID) error
func (s *Service) Unvote(ctx, subj, ideaID) error   // symmetrical (-1 only if deleted)
func (s *Service) List(ctx, viewer *acl.Subject, f ListFilter, cur Cursor, limit int) ([]Idea, string, error)
func (s *Service) GetBySlug(ctx, viewer *acl.Subject, slug string) (Idea, []HistoryItem, canonicalSlug string, error)
```
Публичность: все идеи читаются без авторизации; автор показывается всегда как `nickname` + `@slug` со ссылкой на `/{locale}/people/{slug}` (slug публичен у всех, ТЗ-D08); удалённый автор — «Deleted user» без ссылки.

## 3. messenger

### Модель
```go
type Conversation struct{ ID uuid.UUID; Status string /*request|accepted|declined*/; InitiatedByMe bool; Peer PeerInfo; LastSeq int64; LastMessageAt *time.Time; UnreadCount int; LastMessagePreview string; Muted bool }
type PeerInfo struct{ UserID uuid.UUID; Nickname, Slug string; AvatarURL *string; BlockedByMe bool }
type Message struct{ ID uuid.UUID; ConversationID uuid.UUID; Seq int64; SenderID *uuid.UUID; ClientID uuid.UUID; Body string; CreatedAt time.Time }
type Event struct{ Type string /*"message"|"read"*/; ConversationID uuid.UUID; Message *Message; ReaderID *uuid.UUID; ReadSeq *int64 }
```

### Repo
```go
GetOrCreateDirect(ctx, q, initiator, peer uuid.UUID) (Conversation, created bool, error)   // новый диалог: initiator_id=initiator, status='request'; существующий возвращается как есть
  // low,high := sort(a,b); INSERT ... ON CONFLICT (user_low,user_high) DO NOTHING; затем SELECT. При создании вставить 2 строки conversation_members.
GetConversationForMember(ctx, q, convID, userID) (Conversation, error)         // ErrNotFound если не участник
ListConversations(ctx, q, userID uuid.UUID, folder string /*"inbox"|"requests"|"sent"*/, cur Cursor, limit int) ([]Conversation, error) // ORDER BY COALESCE(last_message_at,created_at) DESC, id DESC
   // inbox: status='accepted'; requests: status='request' AND initiator_id <> userID (входящие запросы); sent: status IN ('request','declined') AND initiator_id = userID
SetStatus(ctx, q, convID uuid.UUID, from string, to string) (bool, error)  // UPDATE .. WHERE status=$from; decided_at=now()
CountInitiatorMessages(ctx, q, convID, senderID uuid.UUID) (int, error)
PendingRequestsCount(ctx, q, userID uuid.UUID) (int64, error); unread = last_seq - last_read_seq
NextSeq(ctx, q, convID) (int64, error)               // UPDATE conversations SET last_seq=last_seq+1, last_message_at=now() WHERE id=$1 RETURNING last_seq
InsertMessage(ctx, q, m Message) error
FindByClientID(ctx, q, convID, senderID, clientID) (Message, error)
ListMessages(ctx, q, convID, beforeSeq *int64, afterSeq *int64, limit int) ([]Message, error) // before: seq<$ ORDER BY seq DESC; after: seq>$ ORDER BY seq ASC
SetMuted(ctx, q, convID, userID uuid.UUID, muted bool) error
UnreadTotal(ctx, q, userID) (int64, error)   // сумма (last_seq - last_read_seq) только по status=accepted AND muted=false
CountConversationsCreatedSince(ctx, q, userID, since time.Time) (int, error)
SetReadSeq(ctx, q, convID, userID, seq int64) error  // UPDATE ... SET last_read_seq = GREATEST(last_read_seq,$3) ... AND $3 <= conversations.last_seq
IsBlocked(ctx, q, a, b uuid.UUID) (bool, error)      // block in either direction
Block(ctx, q, blocker, blocked) error / Unblock(...)  // idempotent
```

### Service
```go
// StartConversation: Require(PermMessageSend). Писать может любой зарегистрированный пользователь любому другому (без подписки и разрешения, ТЗ-D15).
// peer: по user_id; должен существовать, status=active, ≠ self (validation_failed{user_id:invalid_value}). IsBlocked в любую сторону -> forbidden("blocked").
// Антиспам: если диалога ещё нет — лимит НОВЫХ диалогов (fail-closed, Redis rule conv_new): аккаунт младше 24 ч — 5 за 24 ч; остальные — 20 за 24 ч; превышение -> rate_limited. Существующий диалог возвращается без лимита.
func (s *Service) StartConversation(ctx, subj, peerID uuid.UUID) (Conversation, error)

// Send (идемпотентно):
// 1. Require(PermMessageSend); проверить членство в диалоге, текущий user status и блокировки.
// 2. FindByClientID(convID,senderID,clientID): если найдено — вернуть сохранённое Message (HTTP 200)
//    до лимитов для новых операций и проверок request/declined; тело повторного запроса ничего не меняет.
// 3. Для новой отправки TrimSpace body, 1..4000 рун, без управляющих кроме newline.
//    Redis (fail-closed): 30/мин на user; антиспам dup:{sender}:{hash(normalizedBody)} с TTL 1h,
//    одинаковый текст в 5 разных диалогах за час отклонить на пятом. Проверка Redis атомарная.
//    Новый аккаунт <24h без ответов peer не может отправлять URL (https?://|www\.).
// 4. Одна tx: SELECT conversation FOR UPDATE; повторно проверить FindByClientID, статус,
//    участников и блокировки; request инициатору разрешает максимум 3 сообщения (count под lock);
//    request/declined + сообщение получателя -> accepted; declined инициатору -> forbidden.
//    NextSeq; InsertMessage; SetReadSeq(sender,seq); outbox.Add(message.created) без текста;
//    при изменении статуса также outbox.Add(conversation.status_changed).
// 5. COMMIT; relay.Kick. Конфликт уникальности -> откат и чтение прежнего результата.
//    Повтор третьего сообщения после потери HTTP-ответа возвращает то же сообщение,
//    параллельные Send не обходят request cap и не создают дыр в seq.
func (s *Service) Send(ctx, subj, convID uuid.UUID, clientID uuid.UUID, body string) (Message, error)

func (s *Service) History(ctx, subj, convID, beforeSeq *int64, limit int) ([]Message, error)   // limit default 30, max 100; messages returned in ASC seq order
func (s *Service) MarkRead(ctx, subj, convID, seq int64) error   // в транзакции: SetReadSeq + outbox.Add("message.read", payload {reader_id, peer_id, read_seq})
func (s *Service) Conversations(ctx, subj, folder string, cur Cursor, limit int) ([]Conversation, string, error)
// Accept: получатель (не инициатор) переводит request -> accepted. Decline: получатель переводит request -> declined; событие состояния приходит обоим для синхронизации, но не вызывает отдельного уведомления инициатору; его новые отправки получают forbidden "conversation_declined". Повтор идемпотентен. Не-участник -> not_found, инициатор -> forbidden.
func (s *Service) Accept(ctx, subj, convID uuid.UUID) error ; func (s *Service) Decline(ctx, subj, convID uuid.UUID) error
func (s *Service) PendingRequests(ctx, subj) (int64, error)
func (s *Service) BlockUser(ctx, subj, target uuid.UUID) error ; UnblockUser(...)   // блокировка запрещает дальнейшую отправку личных сообщений между двумя пользователями; публичные публикации заблокированного не скрываются автоматически
func (s *Service) SetMuted(ctx, subj, convID uuid.UUID, muted bool) error         // уведомления диалога: не входит в общий счётчик непрочитанных
func (s *Service) UnreadTotal(ctx, subj) (int64, error)
```

### Realtime (SSE) — `messenger/stream.go`
```go
// GET /api/v1/events — requires auth.
// 1. Headers: Content-Type: text/event-stream; Cache-Control: private, no-store, no-transform; X-Accel-Buffering: no. http.Flusher is required.
// 2. Subscribe Redis channel "msg:user:{userID}" BEFORE replay to avoid gaps.
// 3. Replay: if Last-Event-ID header (format "<unixmicro>") is present — nothing special: the client is expected to call GET /conversations and
//    GET /conversations/{id}/messages?after_seq=<lastKnownSeq> after reconnect. Server sends first event `ready` (data: {}).
// 4. For each Redis message write:   event: message|read|conversation\n data: <json>\n\n
// 5. Heartbeat comment ": ping\n\n" every 25 seconds.
// 6. Terminate on r.Context().Done() or on revoked session check every 60s (re-run Resolve; if not authenticated OR Subject.Status!=active -> close).
// 7. Max 5 concurrent streams per user (counter in memory per process; 6th gets 429 rate_limited).
func (h *Handler) Events(w http.ResponseWriter, r *http.Request)
```
Клиент (07) при `ready` и при каждом переподключении догружает пропущенные сообщения по `after_seq`.

## 4. moderation

```go
type Report struct{ ID uuid.UUID; ReporterID *uuid.UUID; TargetType string; TargetID uuid.UUID; Reason, Details string; Snapshot json.RawMessage; Status string; ResolvedBy *uuid.UUID; ResolvedAt *time.Time; ResolutionNote string; CreatedAt time.Time }

// CreateReport: Require(PermReportCreate). target must exist and be visible to reporter:
//   idea/user/article/comment -> через registry.Get: существует и PubliclyVisible(), а у comment его текущая родительская цель также public+published/публичная idea (иначе not_found).
//   message -> reporter must be a conversation member and message sender != reporter; snapshot = up to 5 messages around (2 before, target, 2 after) as [{seq,sender_id,body,created_at}] — this is the ONLY way staff can see private text.
//   idea/article/user -> exists. Duplicate open report from same reporter -> 200 with existing (idempotent). Rate limit 10/day/user.
func (s *Service) CreateReport(ctx, subj, req CreateReportReq) (Report, error)
// ListReports: Require(PermReportView). filter status (default open), cursor. Поле Snapshot в списке НЕ возвращается.
func (s *Service) ListReports(ctx, subj, status string, cur Cursor, limit int) ([]Report, string, error)
// GetReport: Require(PermReportView). Возвращает отчёт со снимком сообщений; каждый просмотр снимка пишет audit "report.view_snapshot" (actor, report id). Модератор не получает доступа к остальной переписке.
// ResolveReport: Require(PermReportResolve). resolution ∈ {resolved, dismissed}; note ≤500. Only from open else conflict. [audit]
func (s *Service) ResolveReport(ctx, subj, id uuid.UUID, resolution, note string) error
// BlockAccount: Require(PermUserBlockAccount). Cannot block self or an admin (forbidden). status=blocked, reason required (≥5 chars), RevokeAll sessions. [audit]
func (s *Service) BlockAccount(ctx, subj, userID uuid.UUID, reason string) error
func (s *Service) UnblockAccount(ctx, subj, userID uuid.UUID) error  // only blocked -> active. [audit]
```

## 5. privacy

```go
// RequestDeletion (DELETE /me): tx с lock 7007 и user row; последнему active admin -> conflict last_admin; иначе active -> pending_deletion, deletion_requested_at=now(); остальные сессии отзываются, текущая сохраняется только для исключений RequireActive; [audit]. Idempotent.
func (s *Service) RequestDeletion(ctx, subj) error
// CancelDeletion (POST /me/restore): pending_deletion -> active. [audit]
func (s *Service) CancelDeletion(ctx, subj) error

// Export (GET /me/export): returns JSON document, Content-Disposition attachment; Rate limit 3/day. [audit]
type ExportDoc struct {
    Profile User; Identities []{Provider, Email, CreatedAt} /* only owner contact; no subject ids/tokens */; Slugs []string
    Ideas []Idea; Votes []{IdeaSlug, At}
    Conversations []{PeerSlug string; Messages []{Seq, Mine bool, Body, At}}  // only messages the user can see in their own conversations
    Reports []Report /* filed by user; no snapshots/staff-only fields */; Comments []Comment; Agreements []Agreement; GeneratedAt time.Time
}
func (s *Service) Export(ctx, subj) (ExportDoc, error)

// PurgeUser (called by job, not by HTTP). One transaction per user:
// 0. Защита последнего active admin под pg_advisory_xact_lock(7007), повторная проверка pending_deletion и срока под user row lock; CancelDeletion не может проиграть гонку с purge.
// 1. users: nickname='Deleted user', bio='', avatar_url=NULL, profile_public=false, status='deleted', locale='bg'.
// 2. auth_identities: DELETE; sessions: DELETE; slugs where entity_type='user' and entity_id=user: DELETE (names become free).
// 3. messages where sender_id=user: body='', deleted_at=now() (rows stay for the peer's seq continuity; peer UI shows "message deleted").
// 4. user_blocks: DELETE where blocker or blocked = user; idea_votes: DELETE and decrement ideas.votes_count by the number removed.
// 5. ideas: author_id=NULL stays (ON DELETE SET NULL is not triggered because the user row is kept) -> explicitly UPDATE ideas SET author_id=NULL WHERE author_id=user.
//    articles.author_id likewise set to NULL. reports.reporter_id set NULL.
// 6. comments автора: body='', status='deleted', author_id=NULL, hidden_by=NULL; registry обновляется.
// 7. user_agreements: DELETE; sessions вместе с IP/UA: DELETE; user.block_reason=NULL,
//    deletion_requested_at=NULL. resources.owner_id для пользователя -> NULL; role_bindings DELETE.
// 8. Поля аудита actor_id/IP для этого пользователя обнуляются; в целях расследования только
//    минимальные несекретные события/ID сохраняются на фиксированный срок (см. ниже).
// 9. S3 author_id в исторических manifest не переписывается ради целостности; псевдонимный ID
//    остаётся без публичной связи. Sync никогда не восстанавливает связь с user.status=deleted.
// 10. audit ActionAccountPurge(meta{}); никакого утверждения о полной анонимности всех копий.
func (s *Service) PurgeUser(ctx, userID uuid.UUID) error
```
Политика хранения [Т], требующая юридической проверки до запуска: тексты идей/статей сохраняются с обезличенным публичным автором; комментарии удаляются как выше. Audit/IP: 90 дней; тело жалоб и снимки переписки — до закрытия + 90 дней, при открытой жалобе максимум 180 дней с создания, затем удалить snapshot/details и персональные связи (отдельная документированная legal hold при необходимости). Это не утверждение о достаточности выбранного основания по GDPR. `CleanupReports` должен обрабатывать также просроченные открытые жалобы; `CleanupAudit` — удалять события старше 90 дней.

S3 и бэкапы могут содержать исторические псевдонимные ID и тексты. Срок жизни копий — 08 §7; доступ ограничен. Purge удаляет относящиеся к пользователю Redis-кэши; событий с текстом в Streams нет. Для outbox identifiers установлен срок очистки, а ещё недоставленные старые события после purge не должны заново раскрывать стёртый body: consumer читает текущее состояние БД. При восстановлении старой копии до открытия трафика повторно применяются удаления из отдельного зашифрованного deletion-журнала. Отдельная legal hold/исключения должны быть утверждены и видимы в Privacy Policy; `TODO-LEGAL` блокирует production.

Экспорт включает только данные владельца и доступные ему сообщения: список его reports не содержит staff snapshot/заметок решения или чужих приватных полей. Поле Identities содержит provider и контакт владельца, без provider subject, секретов и tokens. Добавить экспорт собственных комментариев и текущих согласий. Это технический контракт, не юридическая оценка полноты.

## 6. i18nstore (чтение словарей для API)

```go
type Store struct{ pool *pgxpool.Pool; rdb *redis.Client }
type Meta struct{ Version string; Locales []string; DefaultLocale, FallbackLocale string }
// ActiveVersion: SELECT version FROM i18n_active WHERE id=1 ; кэш в памяти процесса на 30 с (sync.RWMutex), без Redis.
func (s *Store) ActiveVersion(ctx) (string, error)
// Bundle: возвращает (jsonBytes, sha256, version) для локали активной версии; ErrNotFound если локаль не из locales.json или версии нет.
// Кэш: Redis key "i18n:{version}:{locale}" -> bytes, TTL 1h; при промахе читать БД и класть в Redis; Redis недоступен -> читать БД напрямую (fail-open).
func (s *Store) Bundle(ctx, locale string) (body []byte, etag string, version string, err error)
func (s *Store) Meta(ctx) (Meta, error)
```
Handler `/api/v1/i18n/{locale}`: `ETag: "<sha256>"`, `Cache-Control: public, max-age=300`, обрабатывает `If-None-Match` → `304`. Пока нет ни одной активной версии, API возвращает встроенный словарь-заглушку `en` из `go:embed i18nstore/fallback_en.json` (минимум ключей для страницы входа и ошибок) и заголовок `X-I18n-Fallback: 1`.

## 7. seo-entries

`GET /api/v1/seo/sitemap-entries?type=articles|ideas|users&cursor=&limit=` (limit до 500): публичный, кэш 5 мин. Возвращает `{path_slug, updated_at, locales[]}`: для `articles` — опубликованные локали; для `ideas` и `users` (`type=users`: только `profile_public=true`, `status=active`; путь `/people/{slug}`) — `locales: []` (страницы без языка контента, URL строится на фронтенде для каждой локали интерфейса).

## 8. jobs (`internal/jobs`)

Запускаются в процессе `cmd/api` горутинами по таймеру; перед каждым запуском берётся `pg_try_advisory_lock(key)` (ключи: `lockPurge=7001, lockSessions=7002, lockReports=7003, lockOutboxRelay=7004, lockOutboxCleanup=7005, lockRegistry=7006`), чтобы работала только одна реплика. Ошибки логируются, процесс не падает.

| Задание | Период | Действие |
|---|---|---|
| `PurgeDeletedAccounts` | 1 ч | выбрать пользователей `pending_deletion` с `deletion_requested_at < now()-30d` (пачка 50), вызвать `PurgeUser` |
| `CleanupSessions` | 6 ч | `DeleteExpiredSessions(olderThan = now()-7d)` |
| `OutboxCleanup` | 24 ч | удалить опубликованные события старше 7 дней |
| `RegistryReconcile` | 24 ч | пересобрать `resources` из таблиц сущностей (§12) |
| `CleanupReports` | 24 ч | удалить закрытые reports старше 90 дней после закрытия; очистить snapshot/details и персональные связи открытых старше 180 дней; учитывать утверждённый legal hold |

`CleanupAudit`: раз в сутки под lock 7008 удалить события старше 90 дней; не удалять отдельный deletion-журнал до истечения последней резервной копии, к которой он относится.

## 8b. Приём изменений: outbox и реестр ресурсов

(См. §11 и §12 ниже.) Каждый сервис, меняющий данные, в **той же транзакции** вызывает `outbox.Add` (каталог событий в §11.2) и, для типов `user/article/idea/comment`, `registry.Upsert` (§12).

## 8a. comments (во всех комментариях видны nickname и @slug автора)

```go
type CommentAuthor struct{ UserID *uuid.UUID; Nickname, Slug string; AvatarURL *string }  // удалённый автор: Nickname "Deleted user", Slug ""
type Comment struct {
    ID uuid.UUID; TargetType string /*article|idea*/; TargetID uuid.UUID; ParentID *uuid.UUID
    Author CommentAuthor; Body string; Status string /*visible|hidden|deleted*/; ReplyCount int; CreatedAt time.Time
}
// Repo: Insert, GetByID, ListTop(ctx,q,targetType,targetID,cur,limit) (created_at ASC,id ASC), ListReplies(ctx,q,parentID,cur,limit),
//       ListByAuthor(ctx,q,authorID,cur,limit) (DESC; только visible), SetStatus(ctx,q,id,status,hiddenBy *uuid.UUID,clearBody bool), CountByAuthorSince(ctx,q,userID,since)
// Create: Require(PermCommentCreate). Тело: TrimSpace, 1–2000 рун, без управляющих символов кроме \n.
//   Цель: article — только status='published' AND visibility='public' (иначе not_found); idea — любая существующая.
//   parent_id: тот же target, статус visible, и parent.ParentID == nil (ответы только на верхний уровень) иначе conflict("invalid_parent").
//   Лимиты (Redis, fail-closed): 10 в минуту и 100 в сутки на пользователя. Аккаунт младше 24 ч: не более 1 URL в комментарии, иначе validation_failed{body:links_not_allowed}.
//   Упоминания @slug не сохраняются отдельно: фронтенд делает кликабельными подстроки по regexp `(^|[^A-Za-z0-9_-])@([a-z0-9]+(?:-[a-z0-9]+)*)` → `/{locale}/people/{slug}`.
func (s *CommentService) Create(ctx, subj, req CreateCommentReq) (Comment, error)
// List (публично): сначала подтвердить, что цель article всё ещё published+public либо idea существует; иначе 404. top-level комментарии цели + reply_count; status hidden/deleted отдаются с пустым Body (заглушка), чтобы сохранить ветку. Ответы — ListReplies с такой же проверкой текущей цели по parent, без раскрытия закрытых целей.
func (s *CommentService) List(ctx, targetType string, targetID uuid.UUID, cur Cursor, limit int) ([]Comment, string, error)
func (s *CommentService) Replies(ctx, parentID uuid.UUID, cur Cursor, limit int) ([]Comment, string, error)
// Delete: автор (status→deleted, body очищается) или Require(PermCommentModerate). Hide/Unhide: Require(PermCommentModerate), переходы только visible<->hidden (deleted не восстанавливается), status hidden/visible, [audit "comment.hide"/"comment.unhide"].
func (s *CommentService) Delete(ctx, subj, id) error ; Hide(ctx, subj, id) error ; Unhide(ctx, subj, id) error
// ByAuthorSlug (профиль, «Публичная активность»): публично, только visible, на публичные цели; если у автора profile_public=false -> пустой список.
func (s *CommentService) ByAuthorSlug(ctx, slug string, cur Cursor, limit int) ([]CommentWithTarget, string, error)
```
Жалобы на комментарии — через `reports` (`target_type='comment'`). Идеи автора на профиле: `ideas.Service.List` с фильтром `AuthorID` (эндпоинт `GET /users/{slug}/ideas`, только если `profile_public=true`).

## 11. outbox: события и Redis Streams (`internal/platform/outbox`)

### 11.1 Компоненты
```go
type Event struct{ Type, AggregateType string; AggregateID uuid.UUID; Payload map[string]any }   // Payload: только идентификаторы и числа
// Add вставляет строку в outbox_events через переданный DBTX (транзакцию вызывающего сервиса). Откат транзакции откатывает событие.
func Add(ctx context.Context, q db.DBTX, e Event) error

type Relay struct{ pool *pgxpool.Pool; rdb *redis.Client; stream string /*"events:main"*/; batch int /*100*/; kick chan struct{} }
func (r *Relay) Kick()                 // неблокирующая отправка в kick (cap 1)
// Run: цикл; ждёт kick или тик 200 мс. Каждая итерация — ОДНА транзакция:
//   SELECT id,event_type,aggregate_type,aggregate_id,payload FROM outbox_events WHERE published_at IS NULL ORDER BY id LIMIT $batch FOR UPDATE SKIP LOCKED;
//   для каждой строки по порядку: XADD events:main MAXLEN ~ 100000 * id=<outbox id> type=… aggregate_type=… aggregate_id=… payload=<json>;
//   UPDATE outbox_events SET published_at=now() WHERE id = ANY($ids); COMMIT.
// Ошибка XADD: ROLLBACK, attempts++ и last_error (отдельным UPDATE), пауза 1s→2s→…→30s (экспоненциально). Порядок сохраняется (по id). Доставка at-least-once (дубль возможен, если упал COMMIT после XADD) — потребители идемпотентны.
func (r *Relay) Run(ctx context.Context)

type StreamEvent struct{ ID, Type, AggregateType string; AggregateID uuid.UUID; Payload map[string]any; OutboxID int64 }
type Handler func(ctx context.Context, ev StreamEvent) error
type Consumer struct{ rdb *redis.Client; stream, group, name string; handlers map[string]Handler }
// Run: XGROUP CREATE events:main <group> 0 MKSTREAM (BUSYGROUP игнорировать); цикл XREADGROUP GROUP <group> <name> COUNT 50 BLOCK 5000 STREAMS events:main ">";
//   по типу события вызвать Handler (нет handler'а -> XACK без действий). Успех -> XACK. Ошибка -> не ACK (останется в PEL).
//   Каждые 30 с: XAUTOCLAIM для записей с idle > 60 с; если число доставок >= 5 -> XADD events:dead (с причиной) и XACK.
func (c *Consumer) Run(ctx context.Context)
```
Relay и все consumers запускаются в процессе `cmd/api` горутинами; relay — под `pg_try_advisory_lock(7004)` (одна реплика), consumers — по одному имени на процесс (`name = hostname`) в общей группе.

### 11.2 Каталог событий (тип → кто пишет → payload)

| Тип | Пишет | Payload |
|---|---|---|
| `idea.submitted` | ideas.Create | `{author_id}` |
| `idea.status_changed` | ideas.SetStatus | `{from,to,actor_id}` |
| `article.submitted` | articles.Submit | `{actor_id}` |
| `article.published` / `article.unpublished` | articles.Publish / Unpublish | `{rev?,visibility?,actor_id}` |
| `article.revision_registered` | articles.SaveSource, contentsync | `{rev,locale}` |
| `comment.created` | comments.Create | `{target_type,target_id,author_id}` |
| `report.created` | moderation.CreateReport | `{target_type,target_id}` |
| `message.created` | messenger.Send | `{message_id,sender_id,recipient_id,seq}` |
| `message.read` | messenger.MarkRead | `{reader_id,peer_id,read_seq}` |
| `conversation.status_changed` | messenger.Accept/Decline | `{status,actor_id}` |
| `user.deletion_requested` | privacy.RequestDeletion | `{}` |

### 11.3 Потребители MVP
- Группа **`realtime`**: `message.created` → прочитать сообщение из PostgreSQL по `message_id`, опубликовать в Redis Pub/Sub `msg:user:{recipient_id}` и `msg:user:{sender_id}` JSON-событие `message` (формат 05 §6); `message.read` → в `msg:user:{peer_id}` событие `read`; `conversation.status_changed` → событие `conversation` обоим. Обработчики идемпотентны (клиент дедуплицирует по `message.id`). Тело сообщения в Stream не попадает, читается из БД.
- Остальные типы события доставляются в Stream, но потребителей пока не имеют (подключаются позже без изменения доменного кода). Для отладки: CLI `api outbox status` печатает число неопубликованных, возраст самого старого, длину Stream и PEL группы.

### 11.4 Эксплуатация
Задания `jobs`: `OutboxCleanup` (24 ч): `DELETE FROM outbox_events WHERE published_at < now()-interval '7 days'`. Метрики (08 §8): `outbox_unpublished` (gauge), `outbox_oldest_unpublished_seconds`, `stream_pending{group}`. Алерт: oldest > 300 с или `events:dead` растёт.

## 12. registry: единый реестр ресурсов (`internal/registry`)

```go
type Type string // "user" | "article" | "idea" | "comment"
type Status string // pending | active | hidden | removed
type Resource struct{ ID uuid.UUID; Type Type; OwnerID *uuid.UUID; Status Status; Visibility string /*public|restricted|private*/; ParentID *uuid.UUID }
func (r Resource) PubliclyVisible() bool   // Status=="active" && Visibility=="public"

type Repo struct{}
func (Repo) Upsert(ctx, q db.DBTX, r Resource) error     // INSERT .. ON CONFLICT (id) DO UPDATE SET owner_id,status,visibility,parent_id,updated_at=now()
func (Repo) Get(ctx, q db.DBTX, id uuid.UUID) (Resource, error)   // ErrNotFound
```
Правила отображения (функции `registry.FromUser/FromArticle/FromIdea/FromComment`, вызываются сервисами в той же транзакции, что и изменение сущности):

| Сущность | status | visibility |
|---|---|---|
| user | `active`→active; `blocked`/`pending_deletion`→hidden; `deleted`→removed | `profile_public` ? public : restricted |
| article | `draft`/`in_review`→pending; `published`→active; `archived`→hidden | `private` пока не опубликована; после публикации — значение `articles.visibility` |
| idea | всегда active | public |
| comment | `visible`→active; `hidden`→hidden; `deleted`→removed | public; `parent_id` = id статьи/идеи; публичность вычисляется совместно с текущей видимостью родителя |

Использование:
1. `moderation.CreateReport` для `idea/user/article/comment` проверяет цель через `registry.Get` (существует и `PubliclyVisible()`, для comment также проверяется текущая видимость родителя; жалоба на сообщения — отдельной логикой), без SQL по таблицам сущностей.
2. Постоянные ссылки: `GET /api/v1/resources/{id}` (05 §9) → `{type, canonical_path}` (`/people/{slug}`, `/news/{slug}`, `/ideas/{slug}`, для комментария — путь родителя + `#comment-{id}`); фронтенд-маршрут `/{locale}/r/{id}` делает редирект (07 §2). Не `PubliclyVisible()` либо родитель комментария недоступен → `404`; чтение проверяет актуальное состояние родителя, не ждёт 24-часовой сверки. Для скрытого профиля resource-ссылка может возвращать 404, хотя прямая people-ссылка по контракту остаётся доступной.
3. Сверка: задание `jobs.RegistryReconcile` (24 ч) пересобирает `resources` из таблиц сущностей одним `INSERT … ON CONFLICT DO UPDATE … SELECT` на тип и пишет в лог число исправленных строк.

## 9. Тесты (обязательный минимум)

- articles: **editor не может опубликовать** (403), moderator может; публикация без исходного текста → `conflict no_source_text`; **файл, загруженный в S3, не делает статью публичной** (sync → `in_review`, в списке/поиске/slug её нет); новая `rev` опубликованной статьи **не меняет** публичный текст до нового `Publish`; перевод, добавленный к уже утверждённой rev, появляется автоматически; fallback локали (запрошенная → en → source); `restricted` статья: чужому пользователю/гостю `not_found`, назначенному — доступна, не попадает в списки/поиск/sitemap; старый slug → канонический; два параллельных `SaveSource` получают разные `rev`; перезапись существующего файла ревизии → conflict; целостность: подмена файла в S3 (sha256) → `internal`, не отдаётся клиенту; в БД нет тела статьи; поиск находит слово из тела только в утверждённой ревизии; CJK по заголовку/аннотации через триграммы (не по телу); `RefreshLive` корректно собирает набор «только rev = published_rev» по каждой локали.
- comments: комментарий содержит nickname и slug автора; ответ на ответ → `invalid_parent`; комментарий к черновику/restricted статье → `not_found`; лимит 10/мин; новый аккаунт с двумя ссылками → 422; скрытый комментарий отдаётся с пустым телом; удалённый автор → «Deleted user»; чужой не может удалить, модератор может; жалоба на комментарий создаётся.
- messenger (запрос на переписку): первое сообщение создаёт диалог со `status=request`, у получателя он только в папке `requests` и не входит в `UnreadTotal`; инициатор после 3 сообщений получает `409 request_pending`; ответ получателя принимает диалог; после `decline` сообщения инициатора → `403 conversation_declined`; `Accept` инициатором → 403; чужой диалог → 404; блокировка действует независимо.
- messenger (дополнительно): лимит новых диалогов (5/24 ч для аккаунта < 24 ч, 21-й диалог зрелого → 429); одинаковый текст в 5 разных диалогах за час → 429 на 5-м; ссылка от нового аккаунта в первое сообщение → 422; `muted` исключает диалог из `UnreadTotal`; писать незнакомому можно без подписки; `GET report` с снимком пишет audit, список — нет.
- people search: находит по префиксу slug и подстроке nickname, регистр не важен; не возвращает заблокированных, удаляемых, `profile_public=false`; `@business` равно `business`.
- ideas: все переходы (разрешённые и запрещённые); `duplicate` без `duplicate_of`; повторный vote идемпотентен и счётчик корректен (параллельно 20 горутин → ровно +1); лимит 5/сутки; редактирование не автором запрещено.
- messenger: чужой диалог → `not_found`; идемпотентность `client_id` (двойная отправка → одно сообщение, один seq); 20 параллельных Send → seq без дыр и дублей; блокировка в обе стороны; `MarkRead` не уменьшает курсор и не превышает `last_seq`; SSE получает событие обоими участниками и не получает чужое.
- moderation: снимок сообщения содержит ≤5 сообщений и только из диалога репортера; `BlockAccount` отзывает сессии; нельзя заблокировать admin.
- privacy: `PurgeUser` оставляет `messages` строки с пустым `body`, удаляет identities/slugs, идеи без автора; экспорт не содержит чужих данных.
- jobs: две параллельные реплики не выполняют задание одновременно.
- outbox: откат транзакции сервиса не оставляет события; relay публикует в порядке `id` и помечает `published_at`; падение Redis → события накапливаются и доставляются после восстановления без потерь; два relay одновременно → нет дублей благодаря `SKIP LOCKED` и advisory lock; consumer: необработанное сообщение возвращается через `XAUTOCLAIM`, после 5 неудач попадает в `events:dead`; сообщение мессенджера доходит до SSE обоих участников даже при перезапуске consumer'а между `Send` и доставкой; в Stream нет текста сообщения (проверка полей).
- registry: `FromArticle` для всех статусов (таблица); публикация/снятие/скрытие комментария меняют строку реестра в той же транзакции (откат → без изменений); `GET /resources/{id}` для скрытого/приватного → 404; `RegistryReconcile` чинит искусственно испорченную строку; жалоба на недоступный (`hidden`) ресурс → `not_found`.

## 13. Проверки исправленной редакции

Обязательны интеграционные сценарии: прогреть public article/list/search/page, затем restricted/unpublish → следующий запрос гостя не получает текст или аннотацию; комментарии/replies/resource закрытой статьи → 404; Delete comment с миграцией 0013 проходит; deleted нельзя unhide; повтор третьего Send возвращает тот же id/seq, а 10 конкурентных новых Send в request дают максимум 3 записи; последний active-admin не удаляется; после purge в комментариях нет author_id/body и экспорт не раскрывает снимки жалоб. Тесты S3: конкурентные SaveSource не уменьшают manifest.rev; попытка замены human или machine файла не меняет байты/хеш; новая утверждённая ревизия не показывает старый перевод.

Событие conversation: `{type:"conversation",conversation_id,status,actor_id}`; payload статуса допустим как техническое состояние, без текста. Оба участника получают обновление списка, но уведомления на Decline нет. `muted` выключает бейдж/уведомления, а не доставку данных для открытого чата. Клиент на готовности/переподключении сверяет conversations и unread-count. Возобновление SSE не считается гарантией архива событий: PostgreSQL и after_seq — источник восстановления.
