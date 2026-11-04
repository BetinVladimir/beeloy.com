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
