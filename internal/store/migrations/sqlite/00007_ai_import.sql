-- Recipe import (P6): per-household AI settings and import jobs. See
-- docs/plan.md, AI import.

-- +goose Up
CREATE TABLE ai_settings (
    household_id       TEXT PRIMARY KEY REFERENCES households (id) ON DELETE RESTRICT,
    enabled            INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    provider           TEXT NOT NULL CHECK (provider IN ('anthropic', 'openai', 'ollama')),
    endpoint           TEXT NOT NULL,
    model              TEXT NOT NULL,
    api_key_enc        TEXT, -- AES-256-GCM, see ai.Box; never rendered
    allow_fetch_page   INTEGER NOT NULL CHECK (allow_fetch_page IN (0, 1)),
    allow_search_items INTEGER NOT NULL CHECK (allow_search_items IN (0, 1)),
    allow_create_item  INTEGER NOT NULL CHECK (allow_create_item IN (0, 1)),
    tested_at          TEXT,
    test_ok            INTEGER CHECK (test_ok IN (0, 1)),
    updated_by         TEXT NOT NULL,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    FOREIGN KEY (household_id, updated_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE import_jobs (
    id           TEXT PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    user_id      TEXT NOT NULL,
    source_url   TEXT,
    source_text  TEXT,
    status       TEXT NOT NULL CHECK (status IN ('queued', 'running', 'ready', 'failed', 'canceled')),
    steps        TEXT NOT NULL, -- JSON array, read only by Go
    draft        TEXT,          -- JSON, read only by Go
    error_code   TEXT,
    error_detail TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    started_at   TEXT,
    finished_at  TEXT,
    UNIQUE (household_id, id),
    CHECK ((source_url IS NULL) <> (source_text IS NULL)),
    FOREIGN KEY (household_id, user_id) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX import_jobs_user ON import_jobs (household_id, user_id, created_at);
CREATE INDEX import_jobs_status ON import_jobs (status, created_at);

-- +goose Down
DROP TABLE import_jobs;
DROP TABLE ai_settings;
