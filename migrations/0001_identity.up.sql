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
