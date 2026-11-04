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
