-- Initial schema. Timestamps are unix milliseconds (UTC) unless noted.

CREATE TABLE usage_records (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    source                TEXT    NOT NULL,
    external_id           TEXT    NOT NULL,
    device_id             TEXT    NOT NULL DEFAULT '',
    ts                    INTEGER NOT NULL,
    service               TEXT    NOT NULL DEFAULT '',
    model                 TEXT    NOT NULL DEFAULT '',
    project               TEXT    NOT NULL DEFAULT '',
    session               TEXT    NOT NULL DEFAULT '',
    input_tokens          INTEGER NOT NULL DEFAULT 0,
    output_tokens         INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_write_5m_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
    thinking_tokens       INTEGER NOT NULL DEFAULT 0,
    request_count         INTEGER NOT NULL DEFAULT 0,
    is_sidechain          INTEGER NOT NULL DEFAULT 0,
    reported_cost_usd     REAL,
    UNIQUE (source, external_id)
);

CREATE INDEX idx_usage_records_ts      ON usage_records (ts);
CREATE INDEX idx_usage_records_model   ON usage_records (model);
CREATE INDEX idx_usage_records_service ON usage_records (service);
CREATE INDEX idx_usage_records_project ON usage_records (project);

-- Incremental read position per source file (see docs/design.md).
-- mtime is unix nanoseconds, updated_at is unix milliseconds.
CREATE TABLE ingest_state (
    source     TEXT    NOT NULL,
    path       TEXT    NOT NULL,
    "offset"   INTEGER NOT NULL DEFAULT 0,
    size       INTEGER NOT NULL DEFAULT 0,
    mtime      INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (source, path)
);

-- Application settings; value is a JSON document.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL CHECK (json_valid(value))
);

-- User-editable per-model prices (USD per million tokens). NULL means
-- "no override for this component; use the built-in price".
CREATE TABLE price_overrides (
    model                   TEXT PRIMARY KEY,
    input_per_mtok          REAL,
    output_per_mtok         REAL,
    cache_read_per_mtok     REAL,
    cache_write_5m_per_mtok REAL,
    cache_write_1h_per_mtok REAL,
    updated_at              INTEGER NOT NULL
);

-- Estimated subscription limit windows.
CREATE TABLE limit_windows (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    service     TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK (kind IN ('5h', 'weekly')),
    token_limit INTEGER NOT NULL,
    metric      TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE budgets (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    scope            TEXT    NOT NULL CHECK (scope IN ('total', 'service', 'model')),
    scope_value      TEXT    NOT NULL DEFAULT '',
    period           TEXT    NOT NULL CHECK (period IN ('daily', 'monthly')),
    threshold_tokens INTEGER,
    threshold_usd    REAL,
    enabled          INTEGER NOT NULL DEFAULT 1,
    CHECK (threshold_tokens IS NOT NULL OR threshold_usd IS NOT NULL)
);

-- One notification per budget per period (e.g. period_key '2026-11-06' or '2026-11').
CREATE TABLE notifications_sent (
    budget_id  INTEGER NOT NULL REFERENCES budgets (id) ON DELETE CASCADE,
    period_key TEXT    NOT NULL,
    sent_at    INTEGER NOT NULL,
    PRIMARY KEY (budget_id, period_key)
);
