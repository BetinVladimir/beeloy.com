# 05. HTTP API (контракт)

Базовый путь `/api/v1`. Формат ответов/ошибок — 01 §4. Колонка «Доступ»: `pub` — без входа; `auth` — нужна сессия и `Status=active`; `auth*` — сессия (допускается `pending_deletion`); `P` — право ACL (03 §9). Все небезопасные методы проходят CSRF-проверку (Origin + `X-Requested-With: beeloy`). OAuth callback вместо этого проверяет одноразовый state, flow-cookie и параметры провайдера (03 §8). Лимиты — по IP, если не сказано иное.

Типы в JSON: `id` — UUID-строка; время — RFC3339 UTC; `locale` ∈ `locales.json`.

## 1. Системные

| Метод и путь | Доступ | Ответ |
|---|---|---|
| GET `/healthz` (вне `/api/v1`) | pub | `200 {"status":"ok"}` — процесс жив |
| GET `/readyz` | pub | `200` если PostgreSQL и Redis отвечают (`SELECT 1`, `PING`, таймаут 1 с), иначе `503` |

## 2. Авторизация

| Метод и путь | Доступ | Тело / параметры | Ответ / ошибки |
|---|---|---|---|
| POST `/auth/otp/request` | pub, лимит 5/ч на email, 20/ч на IP | `{"email":"a@b.c","locale":"ru"}` | `202 {"data":{"retry_after":60}}` всегда (даже если аккаунта нет). `422` неверный email; `429`; `503` |
| POST `/auth/otp/verify` | pub, лимит 10/ч на IP | `{"email","code":"123456","locale","accept_terms":true,"nickname"?,"slug"?}` — `accept_terms`, `nickname`, `slug` используются только при создании аккаунта; `slug` проверяется (формат, занятость) **до** списания кода | `200 {"data":{"user":Me,"is_new":bool}}` + `Set-Cookie bl_session`. `400 invalid_code`; `403` заблокирован; `422 {accept_terms:required}` (новый аккаунт без согласия), `422 {slug:…}`, `409 slug_taken` |
| POST `/auth/logout` | auth* | — | `204`, сессия отозвана, cookie очищена |
Вход и регистрация — один поток: `verify` для неизвестного email создаёт аккаунт (`is_new=true`).

`Me` = `{"id","nickname","bio","avatar_url","slug","locale","status","profile_public","roles":[{"role","scope_type","scope_id"}],"created_at"}`.

## 2a. Google/Facebook и identity

`provider ∈ {google,facebook}`. Детали в 03 §8; ответы redirect с Location и Set-Cookie Worker передаёт без потерь. Все ответы no-store. Лимит начала flow — 10/мин на IP; callback — 30/мин на IP; provider tokens не входят в JSON.

| Метод и путь | Доступ | Назначение |
|---|---|---|
| GET `/auth/oauth/{provider}/start?return_to=…` | pub | Создать flow-state/flow-cookie; 302 на URL провайдера; return_to очищен |
| GET `/auth/oauth/{provider}/callback?code=…&state=…` | pub с state/flow-cookie | Проверка провайдера; существующая identity → сессия и 303 на return_to; новая → одноразовый onboarding-token в HttpOnly-cookie и 303 на `/{locale}/login?step=onboarding`; ошибка — безопасный redirect без секретов |
| POST `/auth/oauth/complete` | onboarding-cookie + обычный CSRF | `{nickname?,slug?,locale,accept_terms:true}`; создать новый аккаунт и identity одной транзакцией; `200 {data:{user:Me,is_new:true}}` и session cookie; повтор token не допускается |
| GET `/me/identities` | auth | `[{id,provider,created_at}]`, без subject, email и tokens |
| POST `/me/identities/{provider}/link` | auth + CSRF | `{return_to?}`; создать flow для текущего аккаунта; вернуть `{data:{authorization_url}}`; браузер переходит по нему |
| DELETE `/me/identities/{id}` | auth + CSRF | Только собственная identity; `204`; `409 last_identity` если способ последний |

При неизвестном provider — 404. Неверный/повторный state или неподтверждённый ответ провайдера не создаёт сессию или identity. Ошибки наружу — существующие `bad_request`, `forbidden`, `conflict`, `unavailable`, без upstream-содержимого. Авторизация callback на api-site не отключает проверку доверенного ingress.

## 3. Профиль и приватность

| Метод и путь | Доступ | Тело | Ответ |
|---|---|---|---|
| GET `/me` | auth* | — | `200 Me` или `401` |
| PATCH `/me` | auth | любое подмножество `{"nickname"(1–100),"bio"(≤500),"locale","profile_public"}` | `200 Me`; `422` |
| PUT `/me/slug` | auth | `{"slug":"ivan"}` | `200 Me`; `422 {slug:invalid_format\|too_short\|too_long\|reserved}`; `409 {code:"conflict", message:"slug_taken"}` или `"slug_change_too_soon"` |
| GET `/slugs/check?type=user&slug=x` | pub, лимит 30/мин на IP | — | `{"data":{"available":true}}` |
| DELETE `/me` | auth | — | `202 Me` (статус `pending_deletion`); `409 last_admin` для последнего active-admin |
| POST `/me/restore` | auth* | — | `200 Me` |
| GET `/me/export` | auth*, лимит 3/сутки | — | `200` JSON `ExportDoc`, `Content-Disposition: attachment; filename="beeloy-export.json"` |
| GET `/users/{slug}` | pub | — | `200 {"data":{"id","nickname","slug","canonical_slug","avatar_url","bio","created_at","activity_visible"}}`; `404` если пользователь не `active` или slug неизвестен. Если `profile_public=false`: `bio` пуст, `activity_visible=false`, страница `noindex` |
| GET `/users/{slug}/ideas` | pub | `limit`,`cursor` | идеи автора (пусто, если `profile_public=false`) |
| GET `/users/{slug}/comments` | pub | `limit`,`cursor` | комментарии автора с целью (`target_type`, `target_slug`) (пусто, если `profile_public=false`) |

Публичный URL профиля на фронтенде — `/{locale}/people/{slug}`. Если `slug` — старый, возвращается объект с `canonical_slug` ≠ `slug`; фронтенд отвечает `308` на каноничный URL.

## 4. Контент (тексты в S3; состояние и индекс в БД; публикует модератор)

| Метод и путь | Доступ | Параметры / тело | Ответ |
|---|---|---|---|
| GET `/categories` | pub, кэш 5 мин | — | `[{"id","key","names":{"en":".."}}]` |
| GET `/articles` | pub, no-store | `locale`(обяз.), `category`(key), `limit`, `cursor` | `status=published` и `visibility=public`; `ArticleListItem {id,slug,category_key,title,summary,cover_url,published_at,locale,fallback_from}` |
| GET `/me/articles` | auth | `locale`,`limit`,`cursor` | restricted-статьи, доступные пользователю |
| GET `/articles/{slug}` | pub (restricted — auth и доступ); все: `private, no-store` | `locale`(обяз.) | `{"data":ArticleView,"canonical_slug":"…"}`; `ArticleView` содержит `origin`, `visibility`, `available_locales`, `fallback_from`; нет доступа/не опубликована → `404` |
| GET `/search/articles` | pub, лимит 30/мин на IP, no-store | `q`(2–100), `locale`(обяз.), `limit`(≤20), `cursor` | список `ArticleListItem`; `422` при неверном `q` |
| GET `/search/people` | pub, лимит 30/мин на IP | `q`(2–100, ведущий `@` отбрасывается), `limit`(≤20), `cursor` | `[{"id","nickname","slug","avatar_url"}]` |
| GET `/editor/articles` | P article.write | `limit`,`cursor` | список с черновиками (+`status`, `rev`, `published_rev`, `source_locale`, `updated_at`) |
| GET `/editor/articles/{id}` | P article.write или P article.publish | — | `{"data":{"article":Article,"source":{"title","summary","body_md"},"revisions":[{"rev","locale","origin","stale","created_at"}],"viewer_ids":[…]}}` |
| GET `/editor/articles/{id}/revisions/{rev}/{locale}` | P article.write или P article.publish | — | `LocaleFile` (предпросмотр любой версии и языка) |
| POST `/editor/articles` | P article.write (категория) | `{"category_id","source_locale","title_for_slug","slug"?,"cover_url"?}` | `201 Article` (`status=draft`) |
| PATCH `/editor/articles/{id}` | P article.write | `{"category_id"?,"cover_url"?}` | `200 Article` |
| PUT `/editor/articles/{id}/source` | P article.write | `{"title","summary","body_md"}` | `200 {"data":{"rev":4}}` (новая ревизия; публичный текст не меняется) |
| PUT `/editor/articles/{id}/translations/{locale}` | P article.write | query `rev`(необяз.), тело `{"title","summary","body_md"}` | `204` (ручной перевод, `origin=human`); `409 no_source_text`/`exists` |
| POST `/editor/articles/{id}/submit` | P article.write | — | `200 Article` (`draft → in_review`) |
| GET `/mod/articles` | P article.publish | `limit`,`cursor` | очередь: `in_review` и опубликованные с `rev > published_rev` (`ReviewItem`) |
| POST `/mod/articles/{id}/publish` | P article.publish | `{"rev":4,"visibility":"public\|restricted","viewer_ids":[uuid]?}` | `200 Article`; утверждает ревизию `rev`; `409 no_source_text` |
| POST `/mod/articles/{id}/reject` | P article.publish | `{"note":""}` | `200 Article` (`in_review → draft`) |
| POST `/mod/articles/{id}/unpublish` | P article.publish | — | `200 Article` (`archived`) |

Загрузка файлов в S3 сама по себе **ничего не публикует**: Content Sync регистрирует статью как `in_review`, публичной она становится только после `POST /mod/articles/{id}/publish`.

## 5. Ideas

| Метод и путь | Доступ | Тело / параметры | Ответ |
|---|---|---|---|
| GET `/ideas` | pub | `status`,`category`,`sort=new\|top`(default new),`limit`,`cursor`,`mine=1`(только auth) | список `Idea` (без истории) |
| GET `/ideas/{slug}` | pub | — | `{"data":{"idea":Idea,"history":[HistoryItem]},"canonical_slug":"…"}` |
| POST `/ideas` | auth, P idea.create, лимит 5/сутки | `{"title","body","category","locale"}` | `201 Idea`; `429` |
| PATCH `/ideas/{id}` | auth (автор) | `{"title","body","category"}` | `200 Idea`; `403`; `409 not_editable` |
| PUT `/ideas/{id}/vote` | auth | — | `204`; `409 own_idea\|closed` |
| DELETE `/ideas/{id}/vote` | auth | — | `204` |
| POST `/ideas/{id}/status` | P idea.set_status | `{"to":"planned","note":"…","duplicate_of_id":"uuid"?}` | `200 Idea`; `409 invalid_transition`; `422` |
| PUT `/ideas/{id}/reply` | P idea.reply | `{"text":"…"}` | `200 Idea` |

## 5a. Комментарии (к статьям и идеям; везде показываются nickname и @slug автора)

| Метод и путь | Доступ | Тело / параметры | Ответ |
|---|---|---|---|
| GET `/comments` | pub | `target_type`=`article\|idea`, `target_id`, `limit`, `cursor` | `404` если цель сейчас не public+published; `[Comment]` верхнего уровня (старые первыми) с `reply_count`; `Comment = {id,target_type,target_id,parent_id,author:{user_id,nickname,slug,avatar_url},body,status,reply_count,created_at}` |
| GET `/comments/{id}/replies` | pub | `limit`,`cursor` | `[Comment]`; `404` если родительская цель недоступна |
| POST `/comments` | auth, P comment.create, лимит 10/мин и 100/сутки | `{"target_type","target_id","parent_id"?,"body"}` | `201 Comment`; `404` цель недоступна; `409 invalid_parent`; `422` (в т.ч. `links_not_allowed` для аккаунтов младше 24 ч); `429` |
| DELETE `/comments/{id}` | auth (автор) или P comment.moderate | — | `204` |
| POST `/mod/comments/{id}/hide` | P comment.moderate | — | `204` |
| POST `/mod/comments/{id}/unhide` | P comment.moderate | — | `204` |

## 6. Мессенджер

| Метод и путь | Доступ | Тело / параметры | Ответ |
|---|---|---|---|
| GET `/conversations` | auth | `folder`=`inbox`(по умолчанию)`\|requests\|sent`, `limit`,`cursor` | `[Conversation]` (с `unread_count`, `muted`, `last_message_preview` ≤ 80 символов) |
| GET `/conversations/unread-count` | auth | — | `{"data":{"total":3,"requests":1}}` (`total` — без заглушённых и без запросов; `requests` — входящие запросы на переписку) |
| POST `/conversations/{id}/accept` | auth (получатель) | — | `204`; инициатор → `403` |
| POST `/conversations/{id}/decline` | auth (получатель) | — | `204` |
| PUT `/conversations/{id}/mute` | auth (участник) | — | `204` |
| DELETE `/conversations/{id}/mute` | auth (участник) | — | `204` |
| POST `/conversations` | auth, P message.send | `{"user_id":"uuid"}` (любому зарегистрированному активному пользователю, без подписки/разрешения) | `200 Conversation` (существующий) или `201` (новый); `403` blocked; `429` лимит новых диалогов (5/24 ч для аккаунта младше 24 ч, иначе 20/24 ч) |
| GET `/conversations/{id}/messages` | auth (участник) | `before_seq`,`after_seq`,`limit` | `[Message]` по возрастанию `seq`; не участник → `404` |
| POST `/conversations/{id}/messages` | auth, лимит 30/мин | `{"client_id":"uuid","body":"текст"}` | `201 Message` (новое) или `200 Message` (повтор); `403 blocked`/`conversation_declined`; `409 request_pending` (3 сообщения без ответа); `422`; `429` (30/мин, массовая рассылка) |
| POST `/conversations/{id}/read` | auth | `{"seq":123}` | `204` |
| GET `/events` | auth | заголовок `Accept: text/event-stream` | SSE (04 §3), события `ready`, `message`, `read`, `conversation` |
| PUT `/users/{id}/block` | auth | — | `204` |
| DELETE `/users/{id}/block` | auth | — | `204` |

Событие `conversation`: `data: {"type":"conversation","conversation_id":"…","status":"request|accepted|declined","actor_id":"…"}`; приходит обоим участникам для обновления UI, отдельное уведомление об отклонении не создаётся.

Формат SSE-события `message`: `data: {"type":"message","conversation_id":"…","message":{…Message}}`; `read`: `data: {"type":"read","conversation_id":"…","reader_id":"…","read_seq":12}`.

## 7. Жалобы и модерация

| Метод и путь | Доступ | Тело | Ответ |
|---|---|---|---|
| POST `/reports` | auth, лимит 10/сутки | `{"target_type":"message\|idea\|user\|article\|comment","target_id":"uuid","reason":"spam\|abuse\|illegal\|other","details":"≤1000"}` | `201 Report` (поле `snapshot` скрыто для автора) |
| GET `/mod/reports` | P report.view | `status`(open по умолчанию),`limit`,`cursor` | `[Report]` без `snapshot` |
| GET `/mod/reports/{id}` | P report.view | — | `Report` со `snapshot`; просмотр пишет запись аудита |
| POST `/mod/reports/{id}/resolve` | P report.resolve | `{"resolution":"resolved\|dismissed","note":""}` | `200 Report`; `409` |
| POST `/mod/users/{id}/block` | P user.block_account | `{"reason":"≥5 символов"}` | `204` |
| DELETE `/mod/users/{id}/block` | P user.block_account | — | `204` |

## 8. Администрирование

| Метод и путь | Доступ | Тело / параметры | Ответ |
|---|---|---|---|
| GET `/admin/users` | P user.list | `q`(по nickname/slug, ILIKE-префикс),`limit`,`cursor` | `[{"id","nickname","slug","status","roles":[…],"created_at"}]` (без email) |
| POST `/admin/role-bindings` | P role.grant | `{"user_id","role","scope_type","scope_id"?}` | `201 Binding` |
| DELETE `/admin/role-bindings/{id}` | P role.grant | — | `204`; `409 last_admin` |
| GET `/admin/audit` | P audit.view | `actor_id`,`action`,`limit`,`cursor` | `[AuditEvent]` |

## 9. i18n и SEO

| Метод и путь | Доступ | Ответ |
|---|---|---|
| GET `/i18n/meta` | pub, no-store | `{"data":{"version","locales":[..],"default_locale":"bg","fallback_locale":"en"}}` |
| GET `/i18n/{locale}` | pub, ETag, кэш 300 с | плоский JSON `{"key":"text"}` **без обёртки `data`** (исключение из формата) |
| GET `/resources/{id}` | pub, no-store | `{"data":{"type":"article","canonical_path":"/news/my-article"}}` или `404` (недоступный ресурс) |
| GET `/seo/sitemap-entries` | pub, no-store | см. 04 §7 |

## 10. Правила видимости (сводка)

- Приватные ответы (`/me*`, `/conversations*`, `/events`, `/mod/*`, `/admin/*`, `/editor/*`) всегда `Cache-Control: no-store, private`.
- Публичные ответы никогда не содержат email, provider subject/id, IP; публичные nickname/slug не генерируются из email.
- Все изменяемые ответы контента, поиска, профилей, Ideas, комментариев, /resources и sitemap-entries — `private, no-store`, включая 404. Общий кэш только для словарей i18n и categories. Смена public→restricted/unpublish действует для новых запросов после коммита, без TTL ожидания. Ранее уже скачанные клиентом данные невозможно отозвать.
- Чужие диалоги, черновики статей без права → `404`.
- Заблокированный/удаляемый аккаунт не входит в публичные списки и поиск пользователей.
