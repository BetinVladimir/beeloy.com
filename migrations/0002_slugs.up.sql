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
