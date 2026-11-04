# 02. База данных (PostgreSQL)

Файлы миграций создаются в `migrations/` как `NNNN_<name>.up.sql` и `NNNN_<name>.down.sql` (down — `DROP TABLE … ` в обратном порядке). Выполнять миграции строго в порядке номеров. Все таблицы создаются без `IF NOT EXISTS`. Значения перечислений — `text` + `CHECK`. Триггер `updated_at` не используется: сервисы выставляют `updated_at = now()` явно.

## 0001_identity

```sql
CREATE TABLE users (
    id                    uuid PRIMARY KEY,
    nickname          text        NOT NULL CHECK (char_length(nickname) BETWEEN 1 AND 100),
    bio                   text        NOT NULL DEFAULT '' CHECK (char_length(bio) <= 500),
    avatar_url            text,
    locale                text        NOT NULL DEFAULT 'bg',
    status                text        NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active','blocked','pending_deletion','deleted')),
    profile_public        boolean     NOT NULL DEFAULT true,
    block_reason          text,
    deletion_requested_at timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX users_status_idx ON users (status);
CREATE INDEX users_deletion_idx ON users (deletion_requested_at) WHERE status = 'pending_deletion';

CREATE TABLE auth_identities (
    id             uuid PRIMARY KEY,
    user_id        uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider       text        NOT NULL CHECK (provider IN ('email_otp')),   -- only email-OTP in MVP
    subject        text        NOT NULL,          -- lowercase email
    email          text,                          -- lowercase, may be NULL
    email_verified boolean     NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_used_at   timestamptz,
    UNIQUE (provider, subject)
);
CREATE INDEX auth_identities_user_idx  ON auth_identities (user_id);

CREATE TABLE sessions (
    id           uuid PRIMARY KEY,
    user_id      uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   bytea       NOT NULL UNIQUE,     -- sha256(token)
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    user_agent   text        NOT NULL DEFAULT '',
    ip           inet
);
CREATE INDEX sessions_user_idx ON sessions (user_id) WHERE revoked_at IS NULL;
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

-- History of accepted legal documents (versions come from config TERMS_VERSION / PRIVACY_VERSION)
CREATE TABLE user_agreements (
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    doc         text        NOT NULL CHECK (doc IN ('terms','privacy')),
    version     text        NOT NULL,
    accepted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, doc, version)
);
```

## 0002_slugs

```sql
CREATE TABLE slugs (
    entity_type text        NOT NULL CHECK (entity_type IN ('user','idea','article')),
    slug        text        NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND char_length(slug) BETWEEN 3 AND 80),
    entity_id   uuid        NOT NULL,
    is_current  boolean     NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (entity_type, slug)
);
CREATE UNIQUE INDEX slugs_one_current_idx ON slugs (entity_type, entity_id) WHERE is_current;
CREATE INDEX slugs_entity_idx ON slugs (entity_type, entity_id);
```

Длина в БД 3–80; жёсткие пределы по типу проверяются в Go (`slug.Validate`: `user` 3–32, `idea`/`article` 3–80).

## 0003_content

Источник истины текстов статей — S3 (04 §1). Здесь только состояния публикации, реестр версий и поисковые индексы. Тело статьи (`body_md`) в БД **не хранится**. Состояние публикации (`status`, `visibility`, `published_rev`) определяется **только в БД** решением модератора.

```sql
CREATE EXTENSION pg_trgm;

CREATE TABLE categories (
    id         uuid PRIMARY KEY,
    key        text        NOT NULL UNIQUE CHECK (key ~ '^[a-z0-9_]{2,32}$'),
    names      jsonb       NOT NULL,              -- {"en":"News","ru":"Новости"}; ключ "en" обязателен
    sort_order int         NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (names ? 'en')
);

CREATE TABLE articles (
    id             uuid PRIMARY KEY,
    category_id    uuid        NOT NULL REFERENCES categories(id),
    author_id      uuid        REFERENCES users(id) ON DELETE SET NULL,
    source_locale  text        NOT NULL,
    -- Publication state lives ONLY here (never taken from S3). A new upload never publishes anything.
    status         text        NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','in_review','published','archived')),
    visibility     text        NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','restricted')),
    rev            int         NOT NULL DEFAULT 0 CHECK (rev >= 0),            -- latest uploaded/saved source revision
    published_rev  int         NOT NULL DEFAULT 0 CHECK (published_rev >= 0 AND published_rev <= rev), -- revision approved by a moderator (0 = none)
    cover_url      text,
    published_at   timestamptz,                                                -- first publication
    reviewed_by    uuid        REFERENCES users(id) ON DELETE SET NULL,
    reviewed_at    timestamptz,
    s3_fingerprint text        NOT NULL DEFAULT '',  -- set by content-sync: hash of (key,etag) of the article's objects
    synced_at      timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX articles_list_idx ON articles (status, published_at DESC, id DESC);
CREATE INDEX articles_category_idx ON articles (category_id, published_at DESC, id DESC) WHERE status = 'published' AND visibility = 'public';
CREATE INDEX articles_review_idx ON articles (updated_at DESC) WHERE status = 'in_review' OR (status = 'published' AND rev > published_rev);

-- Who may read an article with visibility = 'restricted' (besides staff with article.write / article.publish).
CREATE TABLE article_access (
    article_id uuid NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (article_id, user_id)
);

-- Registry of every uploaded/saved file version (all revisions, not only live ones). No body text.
CREATE TABLE article_revisions (
    article_id    uuid        NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    rev           int         NOT NULL,
    locale        text        NOT NULL,
    origin        text        NOT NULL CHECK (origin IN ('human','machine')),   -- human: shipped with the resource or typed by an editor; machine: Ollama CLI
    model         text,
    title         text        NOT NULL CHECK (char_length(title) BETWEEN 3 AND 200),
    summary       text        NOT NULL CHECK (char_length(summary) BETWEEN 10 AND 400),
    sha256        text        NOT NULL,              -- of the S3 file bytes
    size          int         NOT NULL,
    search_vector tsvector    NOT NULL,              -- to_tsvector('simple', title||' '||summary||' '||body); body itself is not stored
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (article_id, rev, locale)
);

-- LIVE index: exactly what readers see = for each locale the highest revision <= articles.published_rev (copied by RefreshLive).
CREATE TABLE article_locales (
    article_id    uuid        NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    locale        text        NOT NULL,
    rev           int         NOT NULL,
    origin        text        NOT NULL CHECK (origin IN ('human','machine')),
    model         text,
    title         text        NOT NULL,
    summary       text        NOT NULL,
    sha256        text        NOT NULL,
    size          int         NOT NULL,
    search_vector tsvector    NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (article_id, locale)
);
CREATE INDEX article_locales_fts_idx  ON article_locales USING gin (search_vector);
CREATE INDEX article_locales_trgm_idx ON article_locales USING gin ((title || ' ' || summary) gin_trgm_ops);
```

## 0004_acl

```sql
CREATE TABLE role_bindings (
    id         uuid PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text        NOT NULL CHECK (role IN ('editor','moderator','admin')),
    scope_type text        NOT NULL DEFAULT 'global' CHECK (scope_type IN ('global','category')),
    scope_id   uuid        REFERENCES categories(id) ON DELETE CASCADE,
    granted_by uuid        REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((scope_type = 'global' AND scope_id IS NULL) OR (scope_type = 'category' AND scope_id IS NOT NULL)),
    CHECK (role = 'editor' OR scope_type = 'global')
);
CREATE UNIQUE INDEX role_bindings_uq ON role_bindings
    (user_id, role, scope_type, COALESCE(scope_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX role_bindings_user_idx ON role_bindings (user_id);
```

## 0005_ideas

```sql
CREATE TABLE ideas (
    id             uuid PRIMARY KEY,
    author_id      uuid        REFERENCES users(id) ON DELETE SET NULL,
    title          text        NOT NULL CHECK (char_length(title) BETWEEN 5 AND 120),
    body           text        NOT NULL CHECK (char_length(body) BETWEEN 20 AND 5000),
    category       text        NOT NULL CHECK (category IN ('product','community','content','other')),
    lang           text        NOT NULL,                 -- locale of the author's text
    status         text        NOT NULL DEFAULT 'submitted'
                   CHECK (status IN ('submitted','under_review','needs_info','planned','in_progress','delivered','declined','duplicate')),
    team_reply     text        CHECK (team_reply IS NULL OR char_length(team_reply) BETWEEN 1 AND 2000),
    team_reply_by  uuid        REFERENCES users(id) ON DELETE SET NULL,
    team_reply_at  timestamptz,
    duplicate_of   uuid        REFERENCES ideas(id) ON DELETE SET NULL,
    votes_count    int         NOT NULL DEFAULT 0 CHECK (votes_count >= 0),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ideas_new_idx    ON ideas (created_at DESC, id DESC);
CREATE INDEX ideas_top_idx    ON ideas (votes_count DESC, created_at DESC, id DESC);
CREATE INDEX ideas_status_idx ON ideas (status, created_at DESC);
CREATE INDEX ideas_author_idx ON ideas (author_id, created_at DESC);

CREATE TABLE idea_status_history (
    id         uuid PRIMARY KEY,
    idea_id    uuid        NOT NULL REFERENCES ideas(id) ON DELETE CASCADE,
    from_status text,
    to_status  text        NOT NULL,
    actor_id   uuid        REFERENCES users(id) ON DELETE SET NULL,
    note       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idea_status_history_idx ON idea_status_history (idea_id, created_at);

CREATE TABLE idea_votes (
    idea_id    uuid        NOT NULL REFERENCES ideas(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (idea_id, user_id)
);
```

## 0006_messenger

```sql
CREATE TABLE conversations (
    id              uuid PRIMARY KEY,
    user_low        uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_high       uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    initiator_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,   -- who sent the first message / created the dialog (user_low or user_high)
    status          text        NOT NULL DEFAULT 'request' CHECK (status IN ('request','accepted','declined')),
    decided_at      timestamptz,                                                  -- when the recipient accepted/declined
    last_seq        bigint      NOT NULL DEFAULT 0,
    last_message_at timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (user_low < user_high),
    CHECK (initiator_id = user_low OR initiator_id = user_high),
    UNIQUE (user_low, user_high)
);
CREATE INDEX conversations_high_idx ON conversations (user_high);

CREATE TABLE conversation_members (
    conversation_id uuid   NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_id         uuid   NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    last_read_seq   bigint NOT NULL DEFAULT 0,
    muted           boolean NOT NULL DEFAULT false,   -- notifications for this conversation are off (no unread badge, no SSE push)
    PRIMARY KEY (conversation_id, user_id)
);
CREATE INDEX conversation_members_user_idx ON conversation_members (user_id);

CREATE TABLE messages (
    id              uuid PRIMARY KEY,
    conversation_id uuid        NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    seq             bigint      NOT NULL,
    sender_id       uuid        REFERENCES users(id) ON DELETE SET NULL,
    client_id       uuid        NOT NULL,            -- idempotency key from client
    body            text        NOT NULL CHECK (char_length(body) BETWEEN 0 AND 4000),
    deleted_at      timestamptz,                     -- set by account purge; body becomes ''
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, seq),
    UNIQUE (conversation_id, sender_id, client_id)
);

CREATE TABLE user_blocks (
    blocker_id uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    blocked_id uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (blocker_id, blocked_id),
    CHECK (blocker_id <> blocked_id)
);
```

## 0007_moderation

```sql
CREATE TABLE reports (
    id           uuid PRIMARY KEY,
    reporter_id  uuid        REFERENCES users(id) ON DELETE SET NULL,
    target_type  text        NOT NULL CHECK (target_type IN ('message','idea','user','article','comment')),
    target_id    uuid        NOT NULL,
    reason       text        NOT NULL CHECK (reason IN ('spam','abuse','illegal','other')),
    details      text        NOT NULL DEFAULT '' CHECK (char_length(details) <= 1000),
    snapshot     jsonb,                                 -- for target_type='message': up to 5 messages around the target
    status       text        NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','dismissed')),
    resolved_by  uuid        REFERENCES users(id) ON DELETE SET NULL,
    resolved_at  timestamptz,
    resolution_note text     NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reports_status_idx ON reports (status, created_at DESC);
CREATE UNIQUE INDEX reports_dedup_idx ON reports (reporter_id, target_type, target_id) WHERE status = 'open';
```

## 0008_i18n

```sql
CREATE TABLE i18n_releases (
    version      text PRIMARY KEY,                      -- from manifest.version
    manifest     jsonb       NOT NULL,
    status       text        NOT NULL CHECK (status IN ('staged','active','superseded','failed')),
    error        text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    activated_at timestamptz
);

CREATE TABLE i18n_bundles (
    version text  NOT NULL REFERENCES i18n_releases(version) ON DELETE CASCADE,
    locale  text  NOT NULL,
    content jsonb NOT NULL,                             -- flat {"key":"string"}
    sha256  text  NOT NULL,
    PRIMARY KEY (version, locale)
);

CREATE TABLE i18n_active (
    id      smallint PRIMARY KEY CHECK (id = 1),
    version text NOT NULL REFERENCES i18n_releases(version)
);

CREATE TABLE sync_runs (
    id          uuid PRIMARY KEY,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    result      text CHECK (result IN ('ok','noop','failed')),
    version     text,
    error       text
);
CREATE INDEX sync_runs_started_idx ON sync_runs (started_at DESC);
```

## 0009_audit

```sql
CREATE TABLE audit_events (
    id          uuid PRIMARY KEY,
    actor_id    uuid        REFERENCES users(id) ON DELETE SET NULL,
    action      text        NOT NULL,                  -- see list in 03 (audit.Action*)
    target_type text        NOT NULL DEFAULT '',
    target_id   text        NOT NULL DEFAULT '',
    meta        jsonb       NOT NULL DEFAULT '{}',     -- never secrets / message text
    ip          inet,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_created_idx ON audit_events (created_at DESC);
CREATE INDEX audit_events_actor_idx ON audit_events (actor_id, created_at DESC);
```

## 0010_comments_and_people_search

```sql
CREATE TABLE comments (
    id          uuid PRIMARY KEY,
    target_type text        NOT NULL CHECK (target_type IN ('article','idea')),
    target_id   uuid        NOT NULL,                       -- no FK (polymorphic); existence checked in Go
    parent_id   uuid        REFERENCES comments(id) ON DELETE CASCADE,   -- one level of replies only (parent.parent_id must be NULL, checked in Go)
    author_id   uuid        REFERENCES users(id) ON DELETE SET NULL,
    body        text        NOT NULL CHECK (char_length(body) <= 2000),   -- wiped ('') when the author deletes the comment
    status      text        NOT NULL DEFAULT 'visible' CHECK (status IN ('visible','hidden','deleted')),
    CHECK (status <> 'visible' OR char_length(body) >= 1),
    hidden_by   uuid        REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX comments_target_idx ON comments (target_type, target_id, created_at, id);
CREATE INDEX comments_author_idx ON comments (author_id, created_at DESC, id DESC) WHERE status = 'visible';
CREATE INDEX comments_parent_idx ON comments (parent_id);

-- People search (partial match by nickname / slug)
CREATE INDEX users_nickname_trgm_idx ON users USING gin (nickname gin_trgm_ops) WHERE status = 'active' AND profile_public;
CREATE INDEX slugs_user_trgm_idx ON slugs USING gin (slug gin_trgm_ops) WHERE entity_type = 'user' AND is_current;
```

## 0011_outbox_registry

```sql
CREATE TABLE outbox_events (
    id             bigserial   PRIMARY KEY,                 -- delivery order
    event_type     text        NOT NULL,                    -- see catalog in 04 §11
    aggregate_type text        NOT NULL,
    aggregate_id   uuid        NOT NULL,
    payload        jsonb       NOT NULL DEFAULT '{}',       -- identifiers only: never message text, e-mail, tokens
    created_at     timestamptz NOT NULL DEFAULT now(),
    published_at   timestamptz,
    attempts       int         NOT NULL DEFAULT 0,
    last_error     text        NOT NULL DEFAULT ''
);
CREATE INDEX outbox_unpublished_idx ON outbox_events (id) WHERE published_at IS NULL;
CREATE INDEX outbox_published_idx   ON outbox_events (published_at) WHERE published_at IS NOT NULL;

-- Common index of public resources. Entity tables stay the source of truth; services update this table in the same transaction.
CREATE TABLE resources (
    id          uuid        PRIMARY KEY,                    -- equals the entity id
    type        text        NOT NULL CHECK (type IN ('user','article','idea','comment')),
    owner_id    uuid        REFERENCES users(id) ON DELETE SET NULL,
    status      text        NOT NULL CHECK (status IN ('pending','active','hidden','removed')),
    visibility  text        NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','restricted','private')),
    parent_id   uuid,                                       -- comment -> id of its article/idea; otherwise NULL
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX resources_type_idx  ON resources (type, status, visibility);
CREATE INDEX resources_owner_idx ON resources (owner_id);
```

## 0012_seed

Миграция данных (без секретов): таблица `categories` получает 3 записи (`id` — фиксированные UUID, прописать литералами):

| key | names |
|---|---|
| `news` | `{"en":"News"}` |
| `community` | `{"en":"Community"}` |
| `product` | `{"en":"Product"}` |

Первый администратор создаётся не миграцией, а командой `api bootstrap-admin --email <email>` (см. T13): создаёт пользователя и identity `email_otp` (verified) и привязку `admin/global`, если администраторов ещё нет.

## Инварианты (проверяются в тестах миграций)

1. Нельзя создать две identity с одинаковым `(provider, subject)`.
2. Нельзя иметь два текущих slug у одной сущности.
3. Нельзя создать диалог с `user_low >= user_high`.
4. Нельзя дважды отправить сообщение с одним `(conversation_id, sender_id, client_id)`.
5. Нельзя дважды создать открытую жалобу того же автора на тот же объект.
7. В таблицах `articles`/`article_revisions`/`article_locales` нет колонки с телом статьи.
8. `published_rev <= rev` всегда (CHECK).
9. Откат транзакции сервиса откатывает и запись в `outbox_events` (проверяется тестом на каждом сервисе, который пишет события).
6. `down` миграции откатывают всё без ошибок на пустой БД.
