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
