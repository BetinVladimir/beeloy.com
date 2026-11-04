# 03. Backend: платформа, вход по email-OTP, сессии, slug, ACL

Все пути — внутри `backend/internal/`. Сигнатуры обязательны; тела описаны алгоритмом.

## 1. platform

### 1.1 `platform/ids`
```go
func New() uuid.UUID            // uuid.Must(uuid.NewV7())
func Parse(s string) (uuid.UUID, error)
```

### 1.2 `platform/crypto`
```go
func RandomToken(nBytes int) (string, error)         // crypto/rand, base64.RawURLEncoding
func SHA256(b []byte) []byte
func HMACSHA256(secret, msg []byte) []byte
func ConstTimeEqual(a, b []byte) bool                // subtle.ConstantTimeCompare
func RandomDigits(n int) (string, error)             // crypto/rand, uniform digits, leading zeros allowed
```

### 1.3 `platform/ratelimit`
```go
type Limiter struct{ rdb *redis.Client }
type Rule struct{ Name string; Limit int; Window time.Duration }
// Allow: INCR key "rl:{rule.Name}:{id}"; if result==1 then EXPIRE window.
// Returns allowed=false and retryAfter = remaining TTL when count > Limit.
func (l *Limiter) Allow(ctx context.Context, rule Rule, id string) (allowed bool, retryAfter time.Duration, err error)
// Middleware factory. failClosed=true -> on Redis error respond 503 unavailable; false -> allow and log warn.
func (l *Limiter) Middleware(rule Rule, keyFn func(*http.Request) string, failClosed bool) func(http.Handler) http.Handler
```
Стандартные `keyFn`: `ByIP` (из `RealIP`), `ByUser` (user id из контекста, иначе IP).

### 1.4 `platform/mail`
```go
type Message struct{ To, Subject, TextBody string }
type Mailer interface{ Send(ctx context.Context, m Message) error }
type SMTPMailer struct{...}  // go-mail, STARTTLS, timeout 10s
type LogMailer struct{...}   // dev only: slog.Info with To and Subject; body NOT logged unless APP_ENV=dev
```
Письмо с OTP: тема из словаря `mail.otp.subject`, текст `mail.otp.body` с `{code}` и `{minutes}`; язык — `locale` из запроса (fallback `en`). Для письма на сервере используется минимальный встроенный словарь `internal/identity/mailtexts.go` (bg, en, ru, uk), не `i18n_*`.

### 1.5 `platform/httpx`
```go
func JSON(w http.ResponseWriter, status int, data any)              // {"data":data}
func JSONList(w http.ResponseWriter, data any, next string)          // {"data":..., "next_cursor":next|null}
func NoContent(w http.ResponseWriter)
func WriteError(w http.ResponseWriter, r *http.Request, err error)
func DecodeJSON(r *http.Request, dst any) error   // DisallowUnknownFields; 1 object; error -> bad_request
func ClientIP(r *http.Request) netip.Addr
```

### 1.6 `s3store` (`internal/platform/s3store`)
```go
type Object struct{ Key, ETag string; Size int64; LastModified time.Time }
type ObjectStore interface {
    Get(ctx context.Context, key string, maxSize int64) ([]byte, error)   // читает не более maxSize+1 байт; больше -> ErrTooLarge; нет объекта -> ErrNotFound
    Put(ctx context.Context, key string, data []byte, contentType string) error  // перезапись разрешена; ключ обязан начинаться с S3_PREFIX
    List(ctx context.Context, prefix string, fn func(Object) error) error // постранично (1000), останавливается на первой ошибке fn
    Exists(ctx context.Context, key string) (bool, error)
}
var ErrNotFound, ErrTooLarge error
type S3Store struct{...}              // aws-sdk-go-v2, endpoint из S3_ENDPOINT, path-style, таймаут операции 15 с, 3 повтора на сетевые ошибки
type MemStore struct{...}             // для тестов (map + mutex), реализует тот же интерфейс
```
Все ключи формируются относительно `S3_PREFIX` внутри `S3Store` (вызывающий код использует ключи без префикса: `articles/…`, `i18n/…`). Тесты пакетов, использующих S3, работают на `MemStore`; отдельный интеграционный тест `S3Store` — на MinIO (testcontainers).

## 2. identity: модели

```go
// identity/model.go
type UserStatus string
const (
    StatusActive UserStatus = "active"; StatusBlocked = "blocked"
    StatusPendingDeletion = "pending_deletion"; StatusDeleted = "deleted"
)
type User struct {
    ID uuid.UUID; Nickname, Bio string; AvatarURL *string; Locale string
    Status UserStatus; ProfilePublic bool; CreatedAt, UpdatedAt time.Time
    Slug string // current slug, filled by repo join
}
type Provider string
const ProviderEmailOTP Provider = "email_otp" // the only provider in MVP (ТЗ-D06)
type AuthIdentity struct {
    ID, UserID uuid.UUID; Provider Provider; Subject string
    Email *string; EmailVerified bool; CreatedAt time.Time; LastUsedAt *time.Time
}
type Session struct {
    ID, UserID uuid.UUID; TokenHash []byte
    CreatedAt, LastSeenAt, ExpiresAt time.Time; RevokedAt *time.Time
}
```
`Subject` определён в пакете `acl` (чтобы не было цикла импортов) и используется всюду как `acl.Subject`:
```go
// acl/model.go
type Subject struct {
    UserID   uuid.UUID
    Status   string    // users.status
    Bindings []Binding // loaded from DB on each request
}
```
`acl.Subject` помещается в `context` функциями `acl.WithSubject(ctx, s)` / `acl.SubjectFrom(ctx) (Subject, bool)`.

## 3. identity: репозиторий (`identity/repo.go`)

Методы (все принимают `ctx`, `DBTX` как первый параметр после ctx, где нужна транзакция):

```go
CreateUser(ctx, q DBTX, u User) error
GetUserByID(ctx, q DBTX, id uuid.UUID) (User, error)                 // includes current slug via LEFT JOIN slugs
UpdateProfile(ctx, q DBTX, id uuid.UUID, displayName, bio *string, locale *string, profilePublic *bool) error
SetUserStatus(ctx, q DBTX, id uuid.UUID, st UserStatus, reason *string, deletionRequestedAt *time.Time) error

FindIdentity(ctx, q DBTX, p Provider, subject string) (AuthIdentity, error)       // ErrNotFound if none
InsertIdentity(ctx, q DBTX, ai AuthIdentity) error                                 // unique violation -> apperr conflict
TouchIdentity(ctx, q DBTX, id uuid.UUID) error

InsertSession(ctx, q DBTX, s Session, ua string, ip netip.Addr) error
GetSessionByHash(ctx, q DBTX, hash []byte) (Session, error)   // only revoked_at IS NULL AND expires_at > now()
TouchSession(ctx, q DBTX, id uuid.UUID, newExpires time.Time) error
RevokeSession(ctx, q DBTX, id uuid.UUID) error
RevokeAllUserSessions(ctx, q DBTX, userID uuid.UUID) error
DeleteExpiredSessions(ctx, q DBTX, olderThan time.Time) (int64, error)
```
Распознавание нарушения уникальности: `errors.As(err, &pgErr) && pgErr.Code == "23505"` → вспомогательная `db.IsUniqueViolation(err, constraintName string) bool`.

## 4. identity: сессии (`identity/sessions.go`)

```go
type SessionService struct{ repo *Repo; pool *pgxpool.Pool; acl *acl.Repo; cfg SessionConfig }
type SessionConfig struct{ TTL time.Duration /*720h*/; RenewAfter time.Duration /*24h*/; CookieName string /*"bl_session"*/; Secure bool }

// Create: token = RandomToken(32); store SHA256(token); expires = now+TTL; returns raw token (only here!).
func (s *SessionService) Create(ctx, userID uuid.UUID, ua string, ip netip.Addr) (token string, expires time.Time, err error)
// Resolve: hash cookie value, load session, load user; if user.Status in (blocked, deleted) -> not authenticated;
// load bindings via acl.Repo.ListBindings; if now - LastSeenAt > RenewAfter -> TouchSession(expires=now+TTL).
func (s *SessionService) Resolve(ctx, token string) (acl.Subject, Session, error)
func (s *SessionService) Revoke(ctx, sessionID uuid.UUID) error
func (s *SessionService) RevokeAll(ctx, userID uuid.UUID) error
func SetCookie(w http.ResponseWriter, cfg SessionConfig, token string, expires time.Time)
func ClearCookie(w http.ResponseWriter, cfg SessionConfig)
```
Cookie: `Name=bl_session; Path=/; HttpOnly; SameSite=Lax; Secure=cfg.Secure; Expires`. Без `Domain`. Пользователь со статусом `pending_deletion` **может** входить (чтобы отменить удаление), но `Subject.Status` отражает это, и middleware `RequireActive` (применяется ко всем защищённым маршрутам, кроме перечисленных) отвечает `403 forbidden`; исключения: `GET /me`, `POST /me/restore`, `GET /me/export`, `POST /auth/logout`.

Middleware `LoadSession`: если cookie отсутствует/невалидна — продолжать без `Subject`. Невалидную cookie не очищать автоматически.

## 5. identity: OTP (`identity/otp.go`)

Redis-ключи:
- `otp:{email}` → hash `{h: hex(HMAC(code)), attempts: int}` с TTL 10 мин;
- `otp:cool:{email}` → `1` с TTL 60 с.

```go
type OTPService struct{ rdb *redis.Client; mailer mail.Mailer; secret []byte; lim *ratelimit.Limiter; auth *AuthService }

// Request: 
// 1. normalize email (lower, trim); validate with net/mail.ParseAddress, len<=254, no display-name part -> validation_failed{email:invalid_format}.
// 2. rate limits: rule otp_email (5/h by email), otp_ip (20/h by IP). Exceeded -> rate_limited with Retry-After. Redis error -> unavailable.
// 3. if cooldown key exists -> return RetryAfter (do NOT send, do NOT error: same 202 response).
// 4. code = RandomDigits(6); store HMAC(secret, email+":"+code) with attempts=0, TTL 10m; set cooldown 60s.
// 5. send mail (errors are logged, response stays 202 — no account enumeration).
// Always returns retryAfter=60.
func (s *OTPService) Request(ctx, email, locale string, ip netip.Addr) (retryAfter int, err error)

// Precheck: вызывается обработчиком verify ДО Verify, если в запросе есть slug: проверяет формат/доступность slug и не расходует код (чтобы пользователь мог исправить slug и повторить с тем же кодом).
func (s *OTPService) Precheck(ctx, slug string) error

// Verify:
// 1. normalize email; fetch record; missing -> invalid_code.
// 2. HINCRBY attempts; if attempts > 5 -> delete record, invalid_code.
// 3. compare ConstTimeEqual(HMAC(email+":"+code), stored); mismatch -> invalid_code.
// 4. delete record (single use); return email (verified).
func (s *OTPService) Verify(ctx, email, code string) (verifiedEmail string, err error)
```
`invalid_code` — единый ответ для «нет кода», «просрочен», «неверный», «слишком много попыток».

## 6. identity: AuthService (`identity/auth.go`) — единый поток входа и регистрации

```go
type AuthService struct{ pool *pgxpool.Pool; repo *Repo; slugs *SlugService; sessions *SessionService; audit *audit.Service }

// LoginOrRegister — вызывается ТОЛЬКО после успешного OTPService.Verify (email подтверждён).
// Одна транзакция:
// 1. identity := FindIdentity(ProviderEmailOTP, email)
//    a) найдена -> user = GetUserByID; TouchIdentity; перейти к п.3.
// 2. Не найдена -> (требуется opts.AcceptTerms == true, иначе validation_failed{accept_terms:required}) создать пользователя:
//      nickname = opts.Nickname (если задан; 1–100 рун, без управляющих символов) иначе local-part of email (до '@'), ≤ 100 рун; пустой -> "User";
//      locale = переданная locale, если она в locales.json, иначе "bg";
//      slug = opts.Slug (если задан: Validate + IsAvailable, иначе validation_failed{slug:…}/conflict{slug:taken}; проверяется ДО списания OTP-кода, см. OTPService.Precheck) иначе slugs.GenerateForUser(nickname);
//      CreateUser, InsertIdentity{provider email_otp, subject=email, email=email, email_verified=true}, slugs.Assign;
//      InsertAgreement(user, doc="terms", version=cfg.TermsVersion) и doc="privacy" (таблица user_agreements). is_new=true.
//      CreateUser, InsertIdentity{provider email_otp, subject=email, email=email, email_verified=true}, slugs.Assign. is_new=true.
// 3. user.Status == blocked -> forbidden (message "account blocked"), сессия НЕ создаётся. deleted -> invalid_code (аккаунт не раскрывается).
// 4. После коммита: sessions.Create(user.ID, ua, ip); audit "auth.login" (meta{is_new}), без email в meta.
type RegisterOpts struct{ Nickname, Slug string; AcceptTerms bool }
func (a *AuthService) LoginOrRegister(ctx, email, locale string, opts RegisterOpts, ua string, ip netip.Addr) (u User, isNew bool, token string, expires time.Time, err error)
```
Изменение email, привязка других способов входа, соцсети — **вне объёма MVP**. Display name по умолчанию — часть почты до `@`; пользователь меняет его в настройках (локальная часть email — персональные данные, поэтому на публичных страницах показывается только `nickname`, и в настройках при первом входе показывается подсказка `settings.profile.change_nickname_hint`).

## 7. identity: SlugService (`identity/slugs.go`)

```go
type EntityType string  // "user" | "idea" | "article"
var reserved = map[string]struct{}{ /* admin, administrator, api, app, auth, about, beeloy, blog, contact, cookies, edit, editor, help, ideas, login, logout, messages, mod, moderator, me, new, news, org, people, privacy, root, rules, search, settings, sitemap, static, support, system, terms, u, user, users, www */ }

func Validate(t EntityType, s string) error  // user: 3–32, idea/article: 3–80; регистр не учитывается (Normalize); returns apperr.Validation{slug: invalid_format|too_short|too_long|reserved}
func Normalize(s string) string              // lower, trim
// Transliterate: Cyrillic/Latin diacritics -> ASCII via a built-in map (golang.org/x/text/unicode/norm + simple table for ru/uk); remove everything not [a-z0-9]; spaces/underscores -> '-'; collapse '-'; trim '-'.
func Slugify(s string) string

type SlugService struct{ pool *pgxpool.Pool; repo *SlugRepo }
// GenerateForUser: base = Slugify(displayName); if len<3 or reserved -> "user"; then try base, base-2, base-3 … (up to 20), then base-<4 random [a-z0-9]>.
func (s *SlugService) GenerateForUser(ctx, q DBTX, displayName string) (string, error)
func (s *SlugService) GenerateUnique(ctx, q DBTX, t EntityType, source string) (string, error) // same for idea/article, uses source=title
// Assign: INSERT new current slug; if entity already has current -> UPDATE old is_current=false first (same tx). Unique violation -> conflict{slug:taken}.
func (s *SlugService) Assign(ctx, q DBTX, t EntityType, entityID uuid.UUID, slug string) error
// Resolve: returns entity_id and canonical (current) slug for any slug incl. old ones. ErrNotFound if none.
func (s *SlugService) Resolve(ctx, q DBTX, t EntityType, slug string) (entityID uuid.UUID, canonical string, err error)
func (s *SlugService) IsAvailable(ctx, q DBTX, t EntityType, slug string) (bool, error)  // not reserved, valid, not in slugs table at all
// ChangeUserSlug: transaction; checks: Validate; last change (created_at of current slug row) older than 30 days unless the current slug was auto-generated within the user's first 24h (allow first manual change freely: rule = count(slugs where user) == 1 OR last change >=30d);
// then Assign. Returns rate error as conflict "slug_change_too_soon" with Retry info in message.
func (s *SlugService) ChangeUserSlug(ctx, userID uuid.UUID, newSlug string) error
```
Возврат старого slug другому владельцу запрещён (`slugs` хранит строки навсегда; при смене владелец может вернуть **свой** старый slug: `Assign` переключает `is_current` у существующей строки, если она принадлежит той же `entity_id`).

## 9. acl

### 9.1 Модель (`acl/model.go`)
```go
type Role string  // "editor" | "moderator" | "admin"
type ScopeType string // "global" | "category"
type Binding struct{ ID uuid.UUID; Role Role; ScopeType ScopeType; ScopeID *uuid.UUID }
type Perm string
const (
    PermIdeaCreate        Perm = "idea.create"
    PermIdeaSetStatus     Perm = "idea.set_status"
    PermIdeaReply         Perm = "idea.reply"
    PermMessageSend       Perm = "message.send"
    PermReportCreate      Perm = "report.create"
    PermReportView        Perm = "report.view"
    PermReportResolve     Perm = "report.resolve"
    PermArticleWrite      Perm = "article.write"
    PermArticlePublish    Perm = "article.publish"
    PermUserBlockAccount  Perm = "user.block_account"
    PermUserList          Perm = "user.list"
    PermRoleGrant         Perm = "role.grant"
    PermAuditView         Perm = "audit.view"
    PermCommentCreate     Perm = "comment.create"
    PermCommentModerate   Perm = "comment.moderate"
)
type Resource struct{ Type string; OwnerID *uuid.UUID; CategoryID *uuid.UUID }
```

### 9.2 Матрица (в коде как `var rolePerms = map[Role][]Perm`, `memberPerms []Perm`)

| Роль | Права |
|---|---|
| member (неявная, `Status==active`) | idea.create, message.send, report.create, comment.create |
| editor | article.write (с учётом scope; **публиковать не может**) |
| moderator | **article.publish**, idea.set_status, idea.reply, report.view, report.resolve, user.block_account, comment.moderate |
| admin | всё из editor, moderator, а также user.list, role.grant, audit.view |

Публикация статей — функция модератора (решение заказчика: «модератор утверждает публикацию и определяет доступ»); роль `moderator` всегда глобальная, поэтому `article.publish` действует на все категории.

Для `pending_deletion` и `blocked` — **нет никаких прав** (всё `false`).

### 9.3 Authorizer
```go
type Authorizer struct{}
// Can: 
//  1. subject.Status != active -> false.
//  2. perm in memberPerms -> true.
//  3. for each binding b: if perm in rolePerms[b.Role] and
//        (b.ScopeType==global || (b.ScopeType==category && res.CategoryID != nil && *res.CategoryID == *b.ScopeID)) -> true.
//  4. false.
func (a Authorizer) Can(s Subject, p Perm, res Resource) bool
// Require: Can -> nil, иначе ErrForbidden. Для Subject отсутствует -> ErrUnauthorized.
func (a Authorizer) Require(s *identity.Subject, p Perm, res Resource) error
func IsOwner(s Subject, res Resource) bool  // res.OwnerID != nil && *res.OwnerID == s.UserID
```

### 9.4 Репозиторий и сервис выдачи ролей
```go
func (r *Repo) ListBindings(ctx, q DBTX, userID uuid.UUID) ([]Binding, error)
func (r *Repo) InsertBinding(ctx, q DBTX, b Binding, userID, grantedBy uuid.UUID) error  // dup -> conflict
func (r *Repo) DeleteBinding(ctx, q DBTX, id uuid.UUID) (Binding, userID uuid.UUID, error)
func (r *Repo) CountGlobalAdmins(ctx, q DBTX) (int, error)

type GrantService struct{...}
// Grant: Require(PermRoleGrant); role editor may have scope category (category must exist), others only global; audit "role.grant".
func (g *GrantService) Grant(ctx, actor acl.Subject, targetUserID uuid.UUID, role Role, scopeType ScopeType, scopeID *uuid.UUID) (Binding, error)
// Revoke: Require(PermRoleGrant); if binding is admin/global and CountGlobalAdmins()==1 -> conflict "last_admin"; audit "role.revoke".
func (g *GrantService) Revoke(ctx, actor acl.Subject, bindingID uuid.UUID) error
```
Роли читаются из БД при каждом запросе, поэтому отзыв действует немедленно.

## 10. audit

```go
// audit/audit.go
const (
    ActionLogin = "auth.login"; ActionLogout = "auth.logout"; ActionSlugChange = "user.slug_change"; ActionRoleGrant = "role.grant"; ActionRoleRevoke = "role.revoke"
    ActionAccountBlock = "user.block"; ActionAccountUnblock = "user.unblock"
    ActionAccountDeleteRequest = "user.delete_request"; ActionAccountDeleteCancel = "user.delete_cancel"; ActionAccountPurge = "user.purge"
    ActionIdeaStatus = "idea.status"; ActionIdeaReply = "idea.reply"
    ActionCommentHide = "comment.hide"; ActionCommentUnhide = "comment.unhide"; ActionReportViewSnapshot = "report.view_snapshot"; ActionArticleReject = "article.reject"
    ActionArticlePublish = "article.publish"; ActionArticleUnpublish = "article.unpublish"
    ActionReportResolve = "report.resolve"; ActionDataExport = "user.export"; ActionI18nActivate = "i18n.activate"
)
type Event struct{ ActorID *uuid.UUID; Action, TargetType, TargetID string; Meta map[string]any; IP netip.Addr }
func (s *Service) Record(ctx, q DBTX, e Event) error   // запись в той же транзакции, что и действие
func (s *Service) List(ctx, actor acl.Subject, cursor Cursor, limit int, filterActor *uuid.UUID, filterAction string) ([]Event, string, error) // Require(PermAuditView)
```
В `meta` запрещены email целиком, коды, токены, тексты сообщений.

## 11. Тесты (обязательный минимум)

- OTP: успех; неверный код ×5 → код сгорает; повторный запрос в течение 60 с не шлёт письмо; лимит email; Redis down → 503; одинаковый ответ для существующего/нового email.
- AuthService: новый email создаёт пользователя с уникальным slug и `is_new=true`; повторный вход — тот же пользователь; email в разном регистре и с пробелами — тот же пользователь; blocked → forbidden и сессия не создаётся; параллельный первый вход с одним email двумя запросами → один пользователь (уникальность `(provider, subject)`).
- Sessions: просроченная/отозванная → не аутентифицирован; RevokeAll; скользящее продление.
- Slug: reserved, регистр, транслитерация (`"Иван Петров"` → `ivan-petrov`), коллизии (`-2`), старый slug резолвится в канонический, чужой старый slug недоступен, лимит 30 дней.
- ACL: таблица (роль × право × область); blocked/pending_deletion → всё запрещено; editor категории A не может писать в B; нельзя удалить последнего admin.
