CREATE TABLE i18n_releases (
    version      text PRIMARY KEY,                      -- from manifest.version
    manifest     jsonb       NOT NULL,
    status       text        NOT NULL CHECK (status IN ('staged','active','superseded','failed')),
    error        text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    activated_at timestamptz
);

CREATE TABLE i18n_bundles (
    version text  NOT NULL REFERENCES i18n_releases(version) ON DELETE CASCADE,
    locale  text  NOT NULL,
    content jsonb NOT NULL,                             -- flat {"key":"string"}
    sha256  text  NOT NULL,
    PRIMARY KEY (version, locale)
);

CREATE TABLE i18n_active (
    id      smallint PRIMARY KEY CHECK (id = 1),
    version text NOT NULL REFERENCES i18n_releases(version)
);

CREATE TABLE sync_runs (
    id          uuid PRIMARY KEY,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    result      text CHECK (result IN ('ok','noop','failed')),
    version     text,
    error       text
);
CREATE INDEX sync_runs_started_idx ON sync_runs (started_at DESC);
