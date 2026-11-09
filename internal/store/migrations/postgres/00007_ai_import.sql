-- Must stay structurally identical to migrations/sqlite/00007_ai_import.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE ai_settings (
    household_id       uuid PRIMARY KEY REFERENCES households (id) ON DELETE RESTRICT,
    enabled            boolean NOT NULL,
    provider           text NOT NULL CHECK (provider IN ('anthropic', 'openai', 'ollama')),
    endpoint           text NOT NULL,
    model              text NOT NULL,
    api_key_enc        text, -- AES-256-GCM, see ai.Box; never rendered
    allow_fetch_page   boolean NOT NULL,
    allow_search_items boolean NOT NULL,
    allow_create_item  boolean NOT NULL,
    tested_at          timestamptz,
    test_ok            boolean,
    updated_by         uuid NOT NULL,
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    FOREIGN KEY (household_id, updated_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
);

CREATE TABLE import_jobs (
    id           uuid PRIMARY KEY,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    user_id      uuid NOT NULL,
    source_url   text,
    source_text  text,
    status       text NOT NULL CHECK (status IN ('queued', 'running', 'ready', 'failed', 'canceled')),
    steps        text NOT NULL, -- JSON array, read only by Go
    draft        text,          -- JSON, read only by Go
    error_code   text,
    error_detail text,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    started_at   timestamptz,
    finished_at  timestamptz,
    UNIQUE (household_id, id),
    CHECK ((source_url IS NULL) <> (source_text IS NULL)),
    FOREIGN KEY (household_id, user_id) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
);

CREATE INDEX import_jobs_user ON import_jobs (household_id, user_id, created_at);
CREATE INDEX import_jobs_status ON import_jobs (status, created_at);

-- +goose Down
DROP TABLE import_jobs;
DROP TABLE ai_settings;
