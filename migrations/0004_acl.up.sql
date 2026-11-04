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
