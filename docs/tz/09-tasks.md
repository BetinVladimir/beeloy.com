# 09. План задач (выполнять строго по порядку)

Формат: **T##** — название · Зав (зависимости) · Док (источник требований) · КП (критерий приёмки). Общее КП для всех задач — README §1.5. «Док NN §X» = файл ТЗ.

## Фаза 0. Каркас

- **T01 Монорепо.** Зав: —. Док 01 §1. Создать структуру каталогов (в т.ч. apps/web и apps/org с отдельными package.json), contracts/locales.json (26), contracts/glossary.json, минимальный contracts/messages/en.json, make sync-locales; `go mod init beeloy.com/backend`, `.gitignore`, `.editorconfig`, `Makefile` (цели из 01 §1). КП: `make lint` и `make build` проходят на пустом каркасе.
- **T02 Конфиг и ошибки.** Зав: T01. Док 01 §3–4. Контрактные acl.Subject/Binding/Resource/Perm (без identity import). Пакеты `config`, `apperr`, `platform/httpx` (03 §1.5), `platform/logx`. КП: тесты `config.Load` (нет обязательной переменной → ошибка), `WriteError` (каждый код → корректный HTTP и JSON).
- **T03 БД и миграции.** Зав: T02. Док 02, 01 §2. `platform/db` (`DBTX`, `WithTx`, `IsUniqueViolation`, `NewPool`), `cmd/migrate`, все файлы `migrations/0001…0013` буквально по 02. КП: тест миграций (testcontainers): up → down → up; все инварианты 02.
- **T04 Redis, ids, crypto, pagination, ratelimit.** Зав: T02. Док 03 §1.1–1.3, 01 §5. КП: `RandomDigits` (длина, только цифры), `Cursor` round-trip и мусор → ошибка, `Limiter.Allow` (N+1-й запрос отклонён, окно истекает, `Retry-After` > 0).
- **T05 Спайк Cloudflare.** Зав: T01. Док 07 §1 и «Прокси API», 08 §5. Минимальный `apps/web` с OpenNext + Mantine, одна SSR-страница и прокси-обработчик `/api/*` → `API_ORIGIN` (локально — заглушка API на `localhost`, например простой Go/Node-сервер с эндпоинтами `/api/v1/ping`, `/api/v1/events` (SSE, heartbeat) и `/api/v1/echo-cookie`). Проверить локально (`wrangler dev`/`preview`) и записать выводы в `docs/tz/SPIKE-T05.md`: SSR+Mantine; `fetch` к API из Worker; прохождение `Set-Cookie` и `Cookie`; **потоковая передача SSE без буферизации**; передача заголовков `X-Proxy-Secret` и `X-Real-Client-IP`; лимиты CPU/времени. Деплой на реальный Cloudflare — только с разрешения пользователя. КП: файл с выводами и решением «прокси через Worker» или «прямые запросы браузера на api-site» (запасной вариант 08 §5).
- **T06 HTTP-каркас API.** Зав: T02–T04. Док 01 §6. `cmd/api/main.go`, роутер chi, middleware (RequestID, Recoverer, AccessLog, RealIP, SecurityHeaders, MaxBody, CSRF), `/healthz`, `/readyz`, graceful shutdown. КП: тесты middleware (CSRF без заголовка/с неверным Origin → 403; `MaxBody` → 413); `readyz` → 503 при остановленном Redis.

## Фаза 1. Identity (OTP, Google, Facebook)

- **T07 Slug.** Зав: T03. Док 03 §7. КП: тесты Slug из 03 §11 (включая параллельное присвоение → одна успешна, вторая `conflict`).
- **T08 Пользователи и сессии.** Зав: T03, T04. Док 03 §2–4. `identity/model.go`, `repo.go`, `sessions.go`, middleware `LoadSession`, `RequireAuth`, `RequireActive`. КП: тесты Sessions из 03 §11.
- **T09 ACL.** Зав: T08. Док 03 §9. Пакет `acl` (реализация поверх контрактов T02: `Authorizer`, `Repo`, `GrantService`); один lock 7007 защищает последнего active-admin во всех потоках. КП: таблица тестов ACL (≥ 40 строк: роль × право × область).
- **T10 Audit.** Зав: T03, T09. Док 03 §10. КП: `Record` откатывается вместе с транзакцией; `List` без права → forbidden.
- **T11 OTP и Mailer.** Зав: T04, T08. Док 03 §1.4, §5. КП: тесты OTP из 03 §11–12, включая атомарное списание, пятый неверный код, гонку перевыпуска и Redis fail-closed.
- **T12 AuthService, Google/Facebook и linking.** Зав: T07, T08, T10, T11. Док 03 §6. Включает `RegisterOpts` (nickname, slug, accept_terms), запись `user_agreements`, `OTPService.Precheck`. КП: тесты AuthService из 03 §11; новый аккаунт без `accept_terms` → 422; занятый slug при регистрации → 409 до списания кода; согласие записано; Google/Facebook login/onboarding/link/unlink проверены на заглушках (03 §8,12), email не объединяет аккаунты, последний способ входа не удаляется.
- **T13 Handlers identity и bootstrap.** Зав: T06, T09, T12. Док 05 §2–3. Эндпоинты auth (Google/Facebook start/callback/complete, /me/identities link/unlink, otp request/verify с `accept_terms`/`nickname`/`slug`, logout), `/me*`, `/users/{slug}` (включая `activity_visible`), `/slugs/check`; команда `api bootstrap-admin --email`; dev-эндпоинт `/dev/login` только при `APP_ENV=dev`. КП: HTTP-тесты (успех, 401, 403/422); при `APP_ENV=prod` `/dev/login` → 404; старый slug даёт `canonical_slug`; /slugs/check доступен гостю; callback проверяет state, проходит Worker и сохраняет Location/Set-Cookie.

## Фаза 2. Контент (S3 + индекс), i18n, Ideas

- **T14 S3-хранилище.** Зав: T02. Док 03 §1.6. `s3store` (`ObjectStore`, `S3Store`, `MemStore`). КП: тесты `MemStore`; интеграционный тест `S3Store` на MinIO (Get/Put/List/Exists, `ErrNotFound`, `ErrTooLarge`, PutIfAbsent и CAS при конкурентных писателях).
- **T15 Outbox, Redis Streams.** Зав: T03, T04, T14. Док 04 §11, 02 «0011_outbox_registry». Пакет `platform/outbox` (`Add`, `Relay`, `Consumer`), job `OutboxCleanup`, `api outbox status`. КП: тесты outbox из 04 §9 (откат без события, порядок, восстановление Redis, SKIP LOCKED, XAUTOCLAIM, events:dead).
- **T16 Реестр ресурсов.** Зав: T03, T08. Док 04 §12. Пакет `registry` (`Upsert`, `Get`, `FromUser/FromArticle/FromIdea/FromComment`), `GET /resources/{id}`, job `RegistryReconcile`. КП: тесты registry из 04 §9.
- **T17 Articles: формат, repo, service.** Зав: T07, T09, T10, T14, T15, T16. Док 04 §1.1–1.5; сервис пишет `registry.Upsert` и события outbox в транзакциях. `articles/s3layout.go` (валидаторы), `repo.go` (включая `Approve`, `RefreshLive`, `UpsertFromManifest`), `service.go` без поиска. КП: тесты articles из 04 §9 на `MemStore` с атомарными conditional writes, особенно: editor не публикует, upload в S3 не публикует, новая rev не меняет публичный текст, restricted-доступ, целостность sha256, два параллельных `SaveSource` не уменьшают manifest.rev; machine-файл не перезаписывается; live содержит только published_rev, без старых переводов.
- **T18 Articles handlers.** Зав: T13, T17. Док 05 §4 (кроме поиска). Публичные, `/me/articles`, `/editor/*`, `/mod/articles*`. КП: HTTP-тесты (черновики/in_review не видны публично; restricted → 404 чужим и `Cache-Control: private, no-store`; editor вызывает `/mod/articles/{id}/publish` → 403; fallback локали).
- **T19 Поиск статей и интерфейс SearchProvider.** Зав: T18. Док 04 §1.6 (`search.Provider`, `SearchArticles`), 05 §4 (`/search/articles`). КП: поиск по слову из тела утверждённой ревизии; CJK по заголовку/аннотации через триграммы (не по телу); не находит `in_review`, `draft`, `restricted`; `q` из 1 символа → 422; лимит 30/мин.
- **T20 i18nstore и API словарей.** Зав: T03, T06. Док 04 §6, 05 §9. Копия `locales.json` в Go через `make sync-locales` (CI проверяет равенство). КП: ETag/304; нет активной версии → fallback + `X-I18n-Fallback`; неизвестная локаль → 404.
- **T21 Content Sync, часть A (словари).** Зав: T03, T14, T20. Док 06 части A и «Общее». Пакет `contentsync` (UI), `cmd/content-sync` (`run`, `once ui`, `rollback`, `status`). КП: тесты 06 «Тесты части A» (юнит и интеграция с MinIO).
- **T22 Content Sync, часть B (статьи).** Зав: T17, T21. Док 06 часть B. КП: тесты 06 §B.3; обязательно: выложенный в S3 ресурс становится `in_review` и не виден читателям; sync не меняет `status`/`published_rev`.
- **T23 Ideas: repo и service.** Зав: T07, T09, T10, T15, T16. Док 04 §2. КП: тесты ideas из 04 §9 (включая параллельное голосование).
- **T24 Ideas handlers.** Зав: T13, T23. Док 05 §5. КП: HTTP-тесты, включая `409 invalid_transition` для всех запрещённых пар.
- **T25 SEO entries.** Зав: T18, T23. Док 04 §7. КП: приватные профили и неактивные пользователи отсутствуют; у статей — список проиндексированных локалей.
- **T26 Comments.** Зав: T17, T23, T15, T16. Док 04 §8a, 05 §5a. Пакет `comments` (repo, `CommentService`, handlers), права `comment.create`/`comment.moderate`. КП: тесты comments из 04 §9 (nickname и slug автора в каждом комментарии, `invalid_parent`, лимиты, новый аккаунт и ссылки, заглушка скрытого, удалённый автор).
- **T27 Поиск людей и профильная активность.** Зав: T13, T19, T24, T26. Док 04 §1.6, 05 §3–4. `search.Provider` + `PGProvider.SearchPeople`, `GET /search/people`, `GET /users/{slug}/ideas`, `GET /users/{slug}/comments`. КП: тесты people search из 04 §9; `profile_public=false` скрывает человека из поиска и активность профиля.

## Фаза 3. Translator (Ollama, ручной запуск)

- **T28 Ollama-клиент, чанки, валидация.** Зав: T02. Док 10 §2–5. КП: тесты 10 §8 для клиента, `SplitMarkdown`, `ValidateChunk` (Ollama — `httptest`).
- **T29 translator article.** Зав: T14, T17, T28. Док 10 §1, §6. `cmd/translator` (`check`, `article`). КП: тесты 10 §8 для `article` (25 локалей, манифест не изменён, `human` не перезаписан, повторный запуск, ретраи).
- **T30 translator ui.** Зав: T28, T21. Док 10 §7. КП: тесты 10 §8 для `ui` (набор ключей сохранён, манифест не создаётся при провале, без `--publish` в S3 ничего нет); сгенерированный набор проходит `contentsync.ValidateSet`.

## Фаза 4. Мессенджер, модерация, администрирование

- **T31 Messenger: repo и service.** Зав: T09, T15. Док 04 §3 (без SSE). Включает режим запроса на переписку (`request/accepted/declined`, `Accept`, `Decline`, лимит 3 сообщений инициатора), лимит новых диалогов, защиту от массовой рассылки, ограничение ссылок для новых аккаунтов, `muted`, `UnreadTotal`. Событие `message.created`/`message.read`/`conversation.status_changed` пишется в outbox в той же транзакции (прямой Pub/Sub из `Send` запрещён). КП: тесты messenger из 04 §9 (включая дополнительные), кроме SSE.
- **T32 Messenger: SSE и handlers.** Зав: T13, T31. Док 04 §3 «Realtime», §11.3 (consumer `realtime`), 05 §6. КП: доставка не теряется при перезапуске consumer'а; интеграционный тест с двумя потоками (доставка в реальном времени, изоляция, heartbeat < 30 с, лимит 5 потоков, закрытие при отзыве сессии).
- **T33 Moderation.** Зав: T31, T23, T17, T26, T10. Док 04 §4, 05 §7. Жалобы на `message/idea/user/article/comment`, `GetReport` с аудитом просмотра снимка. КП: тесты moderation из 04 §9; список жалоб не содержит `snapshot`; `GET /mod/reports/{id}` пишет `report.view_snapshot`.
- **T34 Admin handlers.** Зав: T09, T10. Док 05 §8. КП: последний admin не удаляется; `GET /admin/users` без email.

## Фаза 5. Приватность, задания, метрики

- **T35 Privacy service.** Зав: T13, T31, T23. Док 04 §5. КП: тесты privacy из 04 §9.
- **T36 Jobs.** Зав: T35. Док 04 §8. КП: advisory lock (две реплики), `PurgeDeletedAccounts` обрабатывает только просроченных.
- **T37 Метрики.** Зав: T06. Док 08 §8. КП: `/metrics` на `:9090`, счётчики растут.

## Фаза 6. Frontend

- **T38 Расширение контрактов и тема.** Зав: T01. Док 07 §1–2.2, §3. Проверить и дополнить созданные в T01 contracts/locales.json, glossary.json и messages/en.json (все пользовательские ключи); в `apps/web/src/contracts`: `api-types.ts`, `idea-transitions.ts`, `fallback-en.json`; в `apps/web/src/ui`: тема Mantine. КП: `pnpm --dir apps/web typecheck`; тест: ровно 26 уникальных кодов, `default` ∈ списка, у каждой записи есть `english_name` и `dir`.
- **T39 Каркас web.** Зав: T05, T38. Док 07 §2–3. `middleware.ts`, `[locale]/layout`, `lib/i18n.ts`, `lib/api.ts`, `lib/locales.ts`, `lib/returnTo.ts`, провайдеры, `Header`, `LocaleSwitcher`, `CookieBanner`, `not-found`, `error`, маршрут `[locale]/r/[id]` (редирект по `GET /resources/{id}`). КП: vitest (`detectLocale`, `createT`, `sanitizeReturnTo`); `build:cf` проходит; `getDir` возвращает `rtl` для тестовой локали с `dir: rtl` (в реальном списке RTL-языков нет); `/` без cookie и заголовков перенаправляет на `/bg`.
- **T40 Auth UI.** Зав: T13, T39. Док 07 §4 (LoginForm, SessionProvider, RequireAuth). КП: компонентные тесты (шаги, ошибки, таймер); e2e (1). На странице входа есть OTP, Google и Facebook; onboarding и безопасное связывание аккаунта проверяются на заглушках.
- **T41 Settings UI.** Зав: T40. Док 07 §4 SettingsTabs (Profile, Privacy). КП: debounce проверки slug; удаление подтверждается словом `DELETE`.
- **T42 News и поиск UI + SEO-хелперы.** Зав: T19, T39. Док 07 §6, §4. `lib/seo.ts`, JSON-LD, страницы news и search. КП: тесты `buildMetadata`; XSS-тест Markdown (`<script>`, `javascript:`); уведомление для `origin=machine`; fallback даёт `noindex`.
- **T43 Sitemap и robots.** Зав: T25, T42. Док 07 §6. КП: 26 локальных URL для статической страницы; hreflang статьи — только проиндексированные локали.
- **T44 Ideas UI.** Зав: T24, T39. Док 07 §4. КП: `VoteButton` (оптимистика + откат), `ModerationActions` (только допустимые переходы); e2e (2).
- **T45 Legal-страницы.** Зав: T39. Док 07 §2. КП: legal-страницы рендерят Markdown из ключей и содержат пометку `TODO-LEGAL` в `fallback-en.json`; ссылки на Terms/Privacy есть в форме входа и в футере.
- **T46 Профиль, комментарии, люди.** Зав: T27, T26, T40, T42. Док 07 §4 (`PeopleProfile`, `AuthorChip`, `CommentThread`, `CommentForm`, `MentionText`, вкладка People в поиске). КП: vitest `MentionText` (`@business-expert` → ссылка `/bg/people/business-expert`, e-mail-подобные строки и `foo@bar` не превращаются, XSS-строки выводятся текстом); компонентные тесты `AuthorChip` (удалённый автор без ссылки) и `CommentForm`; редирект 308 со старого slug на `/people/{canonical}`.
- **T47 Messenger UI.** Зав: T32, T40. Док 07 §5. КП: vitest `useChatStream` (дедупликация, backoff, повтор с тем же `client_id`); e2e (3).
- **T48 Editor, Mod, Admin UI.** Зав: T18, T33, T34, T40. Док 07 §4 (`EditorArticle`, `ModArticleQueue`, `ModArticleReview`, `ReportsTable`, `UsersTable`). КП: страницы недоступны без роли (серверный `notFound`); у редактора нет кнопки публикации; модератор публикует с выбором `public`/`restricted` и списком пользователей; таблица версий×локалей отмечает `human`/`machine`/`stale`.
- **T49 Витрина .org, доступность и extract-i18n-keys.** Зав: T44, T47, T48. Док 07 §2.3, §8–10. Создать apps/org с миссией/about/rules/contact и CTA на .com; canonical org и отсутствие auth-cookie проверяются. КП: скрипт падает при `t('missing')`; axe — 0 серьёзных нарушений на login, news/[slug], ideas, messages, search.

## Фаза 7. Инфраструктура и готовность

- **T50 Dockerfile и Compose.** Зав: T13, T22, T37. Док 08 §2, §4, §9. Образ содержит `api`, `content-sync`, `migrate`, `translator`. КП: `make dev-up` поднимает стек (postgres, redis, mailpit, minio, api, content-sync); `/readyz` → 200; `docker compose config` без ошибок; контейнеры от nonroot; `translator` отсутствует среди сервисов Compose.
- **T51 Caddy и безопасность.** Зав: T50. Док 08 §3. КП: `caddy validate`; в `infra/README.md` инструкция проверки отказа без клиентского сертификата. Реальные сертификаты не создаются.
- **T52 Бэкапы.** Зав: T50. Док 08 §7. КП: `shellcheck` чист; `restore-test.sh` восстанавливает dump и S3 inventory в изолированную среду, проверяет хеши/публикацию/ACL/удаления; отсутствие S3-копии даёт FAIL.
- **T53 CI.** Зав: T03, T39. Док 08 §6. КП: `actionlint` чист; job `migrations` и `go` проходят локальным прогоном команд.
- **T54 Cloudflare-конфигурация.** Зав: T39, T49. Док 08 §5. `wrangler.jsonc` для web и org, `infra/CLOUDFLARE.md` (DNS, маршруты, Origin CA, Authenticated Origin Pulls, Cache Rules) и `infra/OPERATIONS.md` (как оператор выкладывает статью с готовыми переводами в S3 и запускает `translator`). КП: `wrangler deploy --dry-run` без ошибок; деплой не выполняется.
- **T55 Контрактные тесты API↔типы.** Зав: T24, T38. Тест вызывает по эндпоинту каждой группы и сверяет структуру/типы и отсутствие приватных полей с `api-types.ts`. КП: удаление поля в ответе ломает тест.
- **T56 Сквозные негативные тесты безопасности.** Зав: T35, T34. Пакет `backend/internal/e2e`: (1) чужой диалог/сообщения/SSE; (2) moderator-эндпоинты обычным пользователем; (3) CSRF; (4) неизвестный `author_id` в теле отклоняется 400 (DisallowUnknownFields), автор не берётся из клиента; (5) перебор OTP; (6) отозванная роль перестаёт действовать со следующего запроса; (7) заблокированный пользователь не использует старую сессию; (8) приватные поля отсутствуют в публичных ответах (нет подстрок `email`, `subject`, `token_hash`); (9) статья в `draft`/`in_review` и файл, только что выложенный в S3, недоступны по slug, в списке, поиске и sitemap; (10) подмена файла статьи в S3 (изменённый sha256) не отдаётся клиенту; (11) `editor` не может опубликовать; (12) `restricted`-статья недоступна постороннему и гостю, не в поиске/sitemap; (13) модератор не читает чужие сообщения иначе чем через снимок жалобы, и каждый просмотр снимка есть в аудите; (14) заблокированный отправитель не может писать, новый аккаунт упирается в лимит новых диалогов. КП: все 14 исходных сценариев и дополнительные регрессии 04 §13, 03 §12 и 07 §10 проходят; проверять public→restricted/unpublish после прогрева кэша, закрытие комментариев, OTP race, повтор третьего Send, last_admin и S3 immutability.
- **T57 Финальная сверка.** Зав: T01–T56. Обновить корневой `README.md` (запуск, тесты, структура), проверить, что каждый пункт ТЗ-D01…D26 реализован или перечислен в `docs/tz/QUESTIONS.md`; полный прогон `make lint test-go test-web build`. КП: отчёт `docs/tz/DONE.md` — таблица «Задача → статус → заметки».

## Определение «готово» для MVP

Все T01–T57 выполнены; `make lint test-go test-web build` зелёный; e2e (1)–(3) и T56 зелёные; `QUESTIONS.md` без блокирующих вопросов; требования .org и OTP/Google/Facebook выполнены; прежний DONE не заменяет новый прогон регрессий. Запуск в production дополнительно требует (вне ТЗ): юридические тексты, реальные секреты и DNS, подтверждение пользователя.

## Уточнение порядка и повторная приёмка

Номера T01–T57 сохранены для связи с прежними отчётами. T01 создаёт общие данные до T20/T29/T30; T02 создаёт независимые типы ACL до T08; T38 расширяет их frontend-представление. Registry в T16 работает через независимые DTO, не требует реализации сервисов будущих задач. В T03 входят seed 0012 и новая corrective migration 0013. После начала реализации старые миграции не менять.

Для уже существующего кода выполнять только затронутые исправления и связанные тесты по текущим контрактам, а не заново создавать каркас. Исторические статусы DONE сохраняются, но затронутые T03, T06–T18, T20–T22, T26–T32, T35–T36, T39–T56 подлежат повторной проверке перед T57. Добавленные здесь тесты считаются «не проверены», пока есть новый результат запуска; обновление ТЗ само по себе не означает исправление реализации.

Если фактическая существующая миграция 0013 уже занята, не перезаписывать её: выбрать следующий свободный номер и обновить ссылки ТЗ до запуска мигратора. Если внешний provider/платформа не настроены, локальные заглушки подтверждают только кодовый контракт, readiness production остаётся отдельной проверкой.
