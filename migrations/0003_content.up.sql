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
