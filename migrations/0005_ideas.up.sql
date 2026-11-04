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
