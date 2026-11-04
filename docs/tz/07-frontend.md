# 07. Frontend (Next.js + Mantine)

Приложения: `apps/web` (beeloy.com, сообщество) и `apps/org` (beeloy.org, витрина). Тема и общие компоненты — `apps/web/src/ui`; типы API и таблицы — `apps/web/src/contracts`; данные, общие с Go и переводчиком (`locales.json`, `glossary.json`, `messages/en.json`), — корневой каталог `contracts/`.

## 1. Технические решения

- Next.js App Router, TypeScript `strict`, Mantine (v7+) + `@mantine/hooks`, `@mantine/form`, `@mantine/notifications`; иконки `@tabler/icons-react`; Markdown `react-markdown` + `rehype-sanitize`; тесты `vitest`, `playwright`.
- Деплой на Cloudflare Workers: `@opennextjs/cloudflare` + `wrangler.jsonc`. Не использовать Node-API, недоступные в Workers (проверяется спайком T05).
- Тема Mantine (`apps/web/src/ui/theme.ts`): `primaryColor: 'yellow'` (бренд «bee»), `defaultRadius: 'md'`, шрифт системный `Inter, system-ui`, поддержка light/dark (`ColorSchemeScript`, значение в cookie `bl_theme`, необязательная cookie; в `bl_consent` не входит как функциональная).
- Правило: **ни одной пользовательской строки в JSX без `t('key')`**. Ошибки API отображаются через `t('error.' + code)` (ключи `error.not_found`, `error.forbidden`, …, `error.unavailable`, `error.rate_limited`, `error.validation_failed`, `error.invalid_code`, `error.internal`).

## 2. Маршрутизация и локали

`apps/web/src/app/`:

```text
# В app/ нет родительского layout с <html> и нет page для /: middleware выполняет редирект до рендера.
# Единственный root layout для пользовательских страниц — [locale]/layout.tsx.
[locale]/layout.tsx                # <html lang dir>, провайдеры (MantineProvider, I18nProvider, SessionProvider), Header, Footer, CookieBanner
[locale]/page.tsx                  # главная: hero + последние 6 статей + блок Ideas
[locale]/news/page.tsx             # список статей (category filter, "load more")
[locale]/news/[slug]/page.tsx      # статья (тело приходит из API, источник — S3)
[locale]/search/page.tsx           # поиск: вкладки Articles / People (?q=&tab=), noindex
[locale]/ideas/page.tsx            # список идей: фильтры status/category, сортировка new|top
[locale]/ideas/new/page.tsx        # форма подачи (auth)
[locale]/ideas/[slug]/page.tsx     # идея + история + голос + ответ команды
[locale]/people/[slug]/page.tsx    # публичный профиль (+ вкладки: Ideas, Comments)
[locale]/login/page.tsx            # email → код; Google/Facebook; onboarding новой social identity
[locale]/settings/page.tsx         # вкладки: Profile, Privacy (export/delete)
[locale]/messages/page.tsx         # список диалогов (auth)
[locale]/messages/[id]/page.tsx    # диалог
[locale]/editor/page.tsx           # список статей (P article.write)
[locale]/editor/[id]/page.tsx      # редактор: исходный текст, ручные переводы, отправка на проверку
[locale]/mod/articles/page.tsx     # очередь публикации (P article.publish)
[locale]/mod/articles/[id]/page.tsx # проверка версии по языкам, публикация, видимость
[locale]/mod/reports/page.tsx      # жалобы (P report.view)
[locale]/admin/users/page.tsx      # пользователи и роли (P user.list)
[locale]/r/[id]/page.tsx             # постоянная ссылка по UUID: GET /resources/{id} → redirect('/{locale}' + canonical_path); 404 если недоступен; noindex
[locale]/legal/[doc]/page.tsx      # doc ∈ privacy|cookies|terms|rules ; текст из ключей i18n legal.{doc}.body (Markdown)
sitemap.ts, robots.ts              # см. §6
not-found.tsx, error.tsx
../middleware.ts                   # §2.1 (или поддерживаемый установленной версией proxy.ts, один вариант)
```

### 2.1 `middleware.ts`
1. Путь начинается с `/api` или `/_next` или файл с точкой → пропустить.
2. Первый сегмент пути ∈ `locales.json` → пропустить.
3. Иначе `307` на `/{detectLocale(req)}{pathname}{search}`, `Cache-Control: private, no-store`; root / обрабатывается здесь до рендера.
`detectLocale`: cookie `bl_locale` (если валидна) → `Accept-Language` (по убыванию q, сравнение по основному тегу: `pt-BR`→`pt`, `bg-BG`→`bg`; `nb`/`no` не поддерживаются) → `DEFAULT_LOCALE` (`bg`).
Явный выбор в `LocaleSwitcher` пишет cookie `bl_locale` (1 год, `Path=/`, `SameSite=Lax`) и переходит на тот же путь в другой локали. Прямая ссылка `/{locale}/…` **никогда** не переписывается автоопределением.

### 2.2 Локали
`contracts/locales.json`:
```json
{ "default": "bg", "fallback": "en", "source": "en", "locales": [ {"code":"bg","name":"Български","english_name":"Bulgarian","dir":"ltr"}, {"code":"en","name":"English","english_name":"English","dir":"ltr"}, {"code":"ru","name":"Русский","english_name":"Russian","dir":"ltr"} ] }
```
(в файле — все 26 кодов из ТЗ-D11 с собственными и английскими названиями; все `dir: "ltr"`, но поле и поддержка `rtl` в коде сохраняются). `default` — язык корневого редиректа и `x-default`; `fallback` — запасной язык контента; `source` — язык-источник словарей интерфейса. Хелперы `lib/locales.ts`: `isLocale(s): s is Locale`, `getDir(locale)`, `LOCALES`, `DEFAULT_LOCALE`. `generateStaticParams` в `[locale]/layout.tsx` возвращает все локали.

### 2.3 i18n-слой (`lib/i18n.ts`)
```ts
export type Messages = Record<string, string>;
export async function loadMessages(locale: Locale): Promise<Messages>
  // fetch(`${API_ORIGIN}/api/v1/i18n/${locale}`, { next: { revalidate: 300, tags: ['i18n'] } }); ошибка/не 200 -> fallback на встроенный apps/web/src/contracts/fallback-en.json
export function createT(messages: Messages, locale: Locale): (key: string, params?: Record<string, string|number>) => string
  // 1) messages[key]; 2) если params.count: ищет `${key}.${new Intl.PluralRules(locale).select(count)}` затем `${key}.other`; 3) подстановка {name}; 4) нет ключа -> возвращает key (в dev console.warn)
export function formatDate(d: string|Date, locale: Locale, opts?: Intl.DateTimeFormatOptions): string
```
Клиентские компоненты получают `messages` через `I18nProvider` (контекст) и хук `useT()`. Серверные — `const t = createT(await loadMessages(locale), locale)`. Передавать клиенту **только** используемое подмножество ключей не требуется в MVP (весь словарь).

Исходный английский словарь интерфейса — `contracts/messages/en.json` (плоский JSON, правила ключей — 06 §1); остальные 25 языков создаёт CLI `translator ui` (10 §7), `fallback-en.json` — проверенная копия полного en-словаря (чтобы fallback не показывал технические ключи на остальных экранах). `apps/web/src/contracts/i18n-keys.json` — список всех ключей, используемых в коде (поддерживается скриптом `scripts/extract-i18n-keys.ts`, запускается в CI; CI падает, если в коде есть `t('x')`, а ключа `x` нет в `i18n-keys.json`). Этот файл — **спецификация для переводчиков** (то, что должно присутствовать в S3 JSON).

## 3. Клиент API (`lib/api.ts`)

```ts
export class ApiError extends Error { code: string; status: number; fields?: Record<string,string> }
type Opts = { auth?: boolean; method?: string; body?: unknown; query?: Record<string, string|number|undefined>; cache?: RequestCache; next?: { revalidate?: number; tags?: string[] }; signal?: AbortSignal }
// Server side (RSC): base = process.env.API_ORIGIN; пересылает заголовок cookie входящего запроса (из next/headers) только для приватных вызовов (opts.auth=true)
// Browser: base = '' (тот же origin beeloy.com; запросы `/api/*` проксирует Worker, см. «Прокси API» ниже), credentials: 'same-origin', добавляет заголовок X-Requested-With: beeloy для небезопасных методов
export async function api<T>(path: string, opts?: Opts): Promise<{ data: T; next_cursor?: string|null; raw: any }>
```
Правила: ответ не-2xx → `throw new ApiError`; `401` защищённого действия в браузере → редирект на `/{locale}/login?return_to=<текущий путь>`; изменяемый контент и любые авторизованные вызовы используют `cache: 'no-store'`, без revalidate/ISR/unstable_cache. Исключения для i18n/categories заданы явно. SSR auth-вызов проходит тем же доверенным серверным transport с PROXY_SHARED_SECRET; cookie передаётся только при opts.auth=true.

### Прокси API (`src/app/api/[...path]/route.ts`)
Единственная точка выхода браузера к backend; Worker передаёт запрос на `API_ORIGIN` (`https://api-site.beeloy.com`). Экспортирует `GET, POST, PUT, PATCH, DELETE, OPTIONS`. Алгоритм:
1. URL цели: `API_ORIGIN + '/api/' + path.join('/') + search`. Путь должен начинаться с `/api/v1/`, иначе `404` (прокси не открывает других адресов backend).
2. Заголовки запроса — только из списка: `cookie, content-type, accept, accept-language, origin, if-none-match, last-event-id, x-requested-with, x-request-id`. Добавить `X-Proxy-Secret: <PROXY_SHARED_SECRET>` и `X-Real-Client-IP: <значение CF-Connecting-IP входящего запроса>`. Остальные (включая любые `x-proxy-secret`/`x-real-client-ip` от клиента) отбрасываются.
3. `fetch(target, { method, headers, body: req.body (stream, duplex: 'half'), redirect: 'manual', signal: req.signal, cache: 'no-store' })`; ограничения тела — на стороне API (1 МиБ).
4. Ответ: статус как есть; заголовки только из списка `content-type, cache-control, etag, retry-after, set-cookie, location, content-disposition, x-i18n-fallback`; тело — **потоком** (`new Response(upstream.body, …)`) без буферизации (иначе SSE `GET /api/v1/events` не работает). Для `text/event-stream` добавить `Cache-Control: private, no-store, no-transform`.
5. Сетевая ошибка/таймаут до ответа (10 с на заголовки, кроме SSE) → `502` с телом `{"error":{"code":"unavailable","message":"upstream unavailable"}}`.
6. В prod `CF-Connecting-IP` должен быть валидным IP из доверенного окружения Worker; отсутствующий заголовок не заменять клиентским X-Forwarded-For. SSR использует ту же server-only функцию отправки с секретом и валидным IP входящего запроса. Статические i18n-запросы .org не нуждаются в подделке браузерного IP: API предоставляет для них отдельный GET-only публичный ingress без секрета на i18n/categories, с Caddy-проверкой peer и IP, нормализованным Caddy; других обходов auth ingress нет.
7. `PROXY_SHARED_SECRET` хранится как секрет Worker (`wrangler secret`), в клиентский код не попадает.

Функция `sanitizeReturnTo(s: string): string` (`lib/returnTo.ts`): допускает только строку, начинающуюся с одного `/`, без `//`, `\`, управляющих символов, длиной ≤ 200; иначе возвращает `/{locale}`. Используется для `return_to` на странице входа и в редиректах `RequireAuth`.

Типы ответов — `apps/web/src/contracts/api-types.ts` (интерфейсы `Me`, `ArticleListItem`, `ArticleView`, `Idea`, `HistoryItem`, `Conversation`, `Message`, `Report`, `Binding` — зеркало 05). Ручное зеркалирование; расхождение ловится тестами контракта (см. 09 T55).

## 4. Компоненты и поведение

| Компонент (`src/components/`) | Назначение и логика |
|---|---|
| `Header` | логотип, навигация (News, Ideas), `LocaleSwitcher`, переключатель темы, при входе — меню пользователя (Profile, Messages с бейджем непрочитанных, Settings, Editor/Mod/Admin если роль, Logout); без входа — кнопка Login |
| `LocaleSwitcher` | `Menu` со списком 26 языков; выбор → cookie `bl_locale` + навигация на тот же путь |
| `SessionProvider` | на клиенте хранит `Me | null`; общий layout не читает cookie и не запрашивает /me; при старте клиент вызывает /me, 401 означает гостя без redirect; защищённые страницы могут передать initialMe из своей отдельной серверной проверки; `useSession()`; `refresh()` |
| `RequireAuth` | если нет сессии — серверный `redirect('/{locale}/login?return_to=…')` (использовать в страницах auth) |
| `RequireRole` | серверная проверка по `Me.roles`; нет права → `notFound()`; (только UX, сервер всё равно проверяет) |
| `CookieBanner` | при отсутствии cookie `bl_consent`: нижняя панель с текстом `cookie.banner.text`, кнопки «OK» (пишет `bl_consent=v1:necessary`, 180 дней) и ссылка на `/legal/cookies`. Краткоживущие bl_oauth_flow/bl_onboarding описаны как необходимые в Cookie Policy и создаются только при социальном входе; ничего необязательного не грузится в MVP; в коде есть `useConsent()` с категориями `necessary|analytics` (analytics всегда false) |
| `LoginForm` | кнопки Google и Facebook → start flow; шаг 1 OTP: email → `POST /auth/otp/request`; шаг 2: 6-значный `PinInput`, обязательный чекбокс согласия с Terms и Privacy (`login.accept_terms`, ссылки на `/legal/terms` и `/legal/privacy`), необязательный блок «Choose your public name» (`nickname`, `@slug` с проверкой доступности `GET /slugs/check`; для существующего аккаунта игнорируется), `POST /auth/otp/verify`, таймер повторной отправки по `retry_after`; ошибки через `t('error.*')`; успех → `router.replace(return_to)` + `refresh()`; social onboarding использует POST /auth/oauth/complete и те же поля/согласия. |
| `SearchBox`, `SearchResults` | поле в `Header` → `/{locale}/search?q=`; страница с вкладками «Articles» (`GET /search/articles`) и «People» (`GET /search/people`; результат — аватар, nickname, `@slug`, ссылка на `/{locale}/people/{slug}`), «Load more» по `next_cursor`; пустой результат и ошибка 422 (слишком короткий запрос) обрабатываются |
| `ArticleCard`, `ArticleBody` | карточка; тело — `react-markdown` с `rehype-sanitize`; если `origin=machine` — над текстом `Alert` с `t('article.machine_translated_notice')` (запрещены `<script>`, `<iframe>`, `javascript:`-ссылки); внешние ссылки `rel="noopener noreferrer nofollow ugc"` |
| `IdeaCard`, `IdeaForm`, `VoteButton`, `StatusBadge`, `StatusTimeline`, `TeamReply` | `VoteButton` — оптимистичное обновление, откат при ошибке; кнопка отключена, если не вошёл (клик ведёт на login) |
| `ModerationActions` | видна при праве; форма смены статуса показывает только допустимые переходы (таблица зеркалится из `apps/web/src/contracts/idea-transitions.ts`) |
| `ChatList`, `ChatView`, `MessageInput`, `useChatStream` | см. §5. `ChatList` имеет вкладки «Inbox» / «Requests» (бейдж `requests`) / «Sent»; в диалоге-запросе получатель видит баннер «X wants to message you» с кнопками Accept, Decline, Block, Report; инициатор видит «Waiting for reply» и после трёх сообщений заблокированное поле ввода (`error.request_pending`). В `ChatView`/`ChatList`: переключатель «Mute notifications» (`PUT/DELETE /conversations/{id}/mute`), в меню сообщения — «Report», в шапке — «Block». Бейдж непрочитанного в `Header` берётся из `GET /conversations/unread-count` (без заглушённых) и обновляется событиями SSE |
| `PeopleProfile` | страница `/{locale}/people/{slug}`: аватар, nickname, `@slug`, bio, дата регистрации; кнопка «Message» (`POST /conversations` → переход в диалог; скрыта для себя и для гостей — у гостей ведёт на login); вкладки «Ideas» (`GET /users/{slug}/ideas`) и «Comments» (`GET /users/{slug}/comments`); при `activity_visible=false` вкладки скрыты и страница `noindex` |
| `AuthorChip` | везде, где есть автор (комментарий, идея, статья): аватар + nickname + `@slug`, оба кликабельны и ведут на профиль; удалённый автор — текст «Deleted user» без ссылки |
| `CommentThread`, `CommentForm`, `MentionText` | под статьёй и идеей: список (`GET /comments`), «Load more», ответы по `reply_count` (`GET /comments/{id}/replies`), форма для вошедших (для гостей — приглашение войти), удалить/пожаловаться/(для модератора) скрыть. `MentionText` разбирает текст по regexp `(^|[^A-Za-z0-9_-])@([a-z0-9]+(?:-[a-z0-9]+)*)` и превращает совпадения в ссылки `/{locale}/people/{slug}`; остальной текст выводится как обычный текст (без HTML) |
| `ReportDialog` | выбор reason + details; `POST /reports`; для сообщений вызывается из меню сообщения |
| `SettingsTabs` | Profile (nickname с подсказкой `settings.profile.change_nickname_hint`, bio, locale, profile_public, slug с проверкой `GET /slugs/check` с debounce 400 мс), Login methods (GET /me/identities, привязка Google/Facebook и отвязка с защитой last_identity), Privacy (кнопка «Export my data» → скачивание, «Delete account» с подтверждением вводом слова `DELETE` и показом срока 30 дней; при `pending_deletion` — баннер и кнопка «Restore») |
| `EditorArticle` | при создании: категория, `source_locale`, заголовок для slug. Основной блок — исходный текст (title/summary/body_md, Textarea + превью), Save создаёт новую ревизию (публичный текст не меняется). Таблица версий×локалей из `GET /editor/articles/{id}` (`missing` / `human` / `machine` / `stale`); для локали можно вставить ручной перевод. Кнопка «Send to review» (`POST …/submit`). **Кнопок публикации нет.** Подсказка `editor.translations.cli_hint`: машинные переводы создаёт оператор утилитой translator |
| `ModArticleReview` | страница `/mod/articles/[id]`: выбор ревизии, просмотр текста любой локали (`GET /editor/articles/{id}/revisions/{rev}/{locale}`, с пометкой `human`/`machine`), форма публикации: visibility `public` / `restricted` (для restricted — выбор пользователей через поиск людей, список `viewer_ids`), кнопки Publish, Reject (с note), Unpublish |
| `ReportsTable`, `UsersTable` (admin: поиск, выдача/отзыв ролей), `ModArticleQueue` (`GET /mod/articles`) | таблицы с cursor-пагинацией «Load more» |

Каждый экран с данными обязан иметь состояния: загрузка (`Skeleton`), пусто (`t('…empty')`), ошибка (`Alert` + кнопка повторить), нет прав (`not-found`). Формы: метки у всех полей, `aria-label` у иконок-кнопок, видимый фокус (Mantine по умолчанию), клавиатурная навигация, мобильная раскладка (`Container size="md"`, брейкпоинты Mantine).

## 5. Мессенджер на клиенте

`useChatStream()` (клиентский хук, монтируется один раз в `[locale]/layout` для вошедшего пользователя):
1. `new EventSource('/api/v1/events')` (same-origin, cookie отправляется автоматически).
2. Событие `ready` → вызвать `refreshConversations()`; для открытого диалога — `GET …/messages?after_seq=<lastSeq>` и слить без дублей (по `id`).
3. `message` → добавить в стор (`zustand`); если диалог открыт и вкладка видима — `POST …/read {seq}` с debounce 500 мс; для чужого нового сообщения обновить unread по контракту accepted+!muted, без дублей. `read` → обновить индикатор «прочитано»; `conversation` → обновить status/вкладки и заново запросить unread-count, без уведомления на declined.
4. `onerror` → закрыть и переподключиться с экспоненциальной задержкой 1,2,4,8,16,30 с (max 30), с jitter ±20%. EventSource не предоставляет HTTP-статус в onerror: после разрыва проверить /me через fetch; 401 или status!=active → закрыть поток, иначе переподключиться. Не создавать цикл login-редиректов для гостя.
5. Отправка: генерируется `client_id = crypto.randomUUID()`, сообщение сразу показывается как `pending`; `POST`; успех → заменить по `client_id`; сетевая ошибка → статус `failed` с кнопкой «Retry», которая **повторяет тот же `client_id`**.
6. Порядок отображения — по `seq`; pending — в конце.
7. Пагинация вверх: `before_seq=<минимальный seq>`, limit 30.

## 6. SEO

- `lib/seo.ts`:
  ```ts
  export function buildMetadata(args: { locale: Locale; path: string /* без префикса локали, начинается с "/" или "" */; title: string; description: string; availableLocales?: Locale[]; noindex?: boolean; ogImage?: string; type?: 'website'|'article' }): Metadata
  ```
  Формирует: `title`, `description`, `alternates.canonical = SITE_URL/{locale}{path}`, `alternates.languages` (hreflang) **только** для `availableLocales` (для статьи — переводы ровно published_rev; для остальных страниц — все локали интерфейса) + `x-default` → `/{default}{path}`; Open Graph (`og:locale`, `og:type`, `og:title`, `og:description`, `og:url`, `og:image`), Twitter card.
- Статья в локали без перевода (`fallback_from` задан): `generateMetadata` ставит canonical на версию реально показанной локали, `robots: noindex`, а страница показывает уведомление `article.fallback_notice`. Страницы с `origin=machine` индексируются (решение ТЗ-D20), `hreflang` строится по `available_locales`.
- Статья рендерится для SEO сервером, с актуальной проверкой видимости и no-store. Для restricted передать cookie только из защищённого вызова; чужой/гость получает 404 без тела, title и аннотации. `noindex` для restricted и `/search`; кэшировать 404 закрытой статьи тоже нельзя.
- JSON-LD: `NewsArticle` только на `/news/[slug]` (`headline`, `datePublished`, `dateModified`, `inLanguage`, `author`, `image`); `Organization` на главной. Вставка через `<script type="application/ld+json">` с экранированием `<`.
- `noindex` для: `/search`, `/login`, `/settings`, `/messages*`, `/editor*`, `/mod*`, `/admin*`, `/ideas/new`.
- `robots.ts`: `Allow: /`; `Disallow: /api/`, и перечисленные приватные пути для каждой локали; `Sitemap: SITE_URL/sitemap.xml`.
- `sitemap.ts` через `generateSitemaps()`: страницы по 5000 URL; собирает из `GET /seo/sitemap-entries` (articles, ideas, users; профили — `/people/{slug}`) и статических страниц (home, news, ideas, legal) для **каждой** из 26 локалей; для статей `alternates.languages` только из `locales` записи. `lastModified` из `updated_at`.
- Редирект старых slug: страница получает `canonical_slug`; если ≠ slug → `permanentRedirect('/{locale}/news/{canonical}')`.

## 7. Безопасность фронтенда

- CSP через `middleware.ts`/headers: `default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'` (inline нужен Next/Mantine; перейти на nonce, если спайк T05 покажет возможность); `frame-ancestors 'none'`; `base-uri 'self'`; `form-action 'self'`.
- Никаких токенов в `localStorage`. Сессионная cookie недоступна JS.
- Приватные страницы рендерятся динамически (`export const dynamic = 'force-dynamic'`), ответы с `Cache-Control: private, no-store`; страницы контента/профилей/Ideas/поиска и их метаданные также динамические/no-store, чтобы смена видимости действовала на новый запрос. Статические assets и витрина могут кэшироваться; кэшируемое HTML не содержит персональных данных. Тема применяется на клиенте.
- `Header`'s меню пользователя подгружается **клиентом** (`SessionProvider` запрашивает `GET /me` после гидратации), для независимого меню без персональных данных в общем layout; серверный `GET /me` используется только в auth-страницах.

## 8. Тесты

- vitest: `createT` (подстановка, plural, отсутствующий ключ), `detectLocale` (таблица случаев), `buildMetadata` (canonical, hreflang только для доступных), `ApiError` разбор, `useChatStream` (мок EventSource: дедупликация, переподключение с backoff), `ValidateReturnTo`-аналог на клиенте не требуется.
- playwright (против dev-стека Compose + Mailpit): (1) вход по OTP, смена slug, выход; (2) подача идеи → голос другим пользователем → смена статуса модератором → виден в списке; (3) два браузерных контекста обмениваются сообщениями в реальном времени, после «offline»-перерыва догружаются пропущенные.
- Доступность: `axe` на страницах login, news/[slug], ideas, messages (0 серьёзных нарушений).

## 9. Витрина beeloy.org

`apps/org` — отдельное Next.js-приложение в том же монорепозитории. Использует `contracts/locales.json` и словари через публичный i18n API .com (при недоступности — встроенный полный en). Маршруты `/{locale}/`, `/about`, `/rules`, `/contact`: миссия, краткое описание проекта, правила/ссылка на полные правила сообщества, контакт и CTA на `https://beeloy.com/{locale}`. Исходный текст миссии и точные формулировки не выдумывать: до предоставления использовать явные редакционные placeholders, отличные от юридических TODO-LEGAL.

Canonical и sitemap — на beeloy.org; правила сообщества целиком живут на .com, витрина ссылается на них. Авторизация, профили, Ideas, мессенджер и auth-cookie на .org не создаются. Переход в .com сохраняет locale в URL, не пытается читать cookie .com. Аналитики/рекламы и cookie-баннера нет при отсутствии необязательных cookie; явный язык можно передавать URL. Общий логотип/визуальная тема должны совпадать, но это не основание считать жёлтый цвет или шрифт исторически утверждённым брендом.

## 10. Приёмка исправленных потоков

Тесты проверяют Google/Facebook buttons + onboarding и linking на заглушках; публичную проверку slug без сессии; единственный html с lang/dir; отсутствие server /me в общем layout; no-store на article HTML/RSC/metadata и прокси; после public→restricted/unpublish следующий гостевой запрос не содержит прежнего текста/аннотации; отсутствие старых переводов в hreflang после новой публикации; обновления conversation при Accept/Decline; muted-чат получает данные без бейджа; org не устанавливает auth-cookie и имеет собственный canonical.

Конкретный файл middleware.ts или proxy.ts выбирается по уже установленной версии Next.js/OpenNext; прежний SPIKE — свидетельство только локального прогона. Не менять версии ради переименования файла; нельзя одновременно держать два конкурирующих обработчика locale.
