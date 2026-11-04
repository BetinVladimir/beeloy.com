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
