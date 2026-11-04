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
