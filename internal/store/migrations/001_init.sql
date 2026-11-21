CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);

CREATE TABLE sessions (
    token_hash BYTEA PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE hosts (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    address     TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE monitors (
    id                   BIGSERIAL PRIMARY KEY,
    host_id              BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    name                 TEXT NOT NULL,
    type                 TEXT NOT NULL CHECK (type IN ('http','ping','traceroute','dns','smtp','tls','tcp')),
    target               TEXT NOT NULL DEFAULT '',
    port                 INTEGER NOT NULL DEFAULT 0 CHECK (port BETWEEN 0 AND 65535),
    interval_seconds     INTEGER NOT NULL DEFAULT 60 CHECK (interval_seconds BETWEEN 10 AND 86400),
    timeout_seconds      INTEGER NOT NULL DEFAULT 10 CHECK (timeout_seconds BETWEEN 1 AND 120),
    retries              INTEGER NOT NULL DEFAULT 1 CHECK (retries BETWEEN 0 AND 10),
    enabled              BOOLEAN NOT NULL DEFAULT TRUE,
    config               JSONB NOT NULL DEFAULT '{}'::jsonb,
    status               SMALLINT NOT NULL DEFAULT 0,
    last_checked_at      TIMESTAMPTZ,
    last_latency_ms      DOUBLE PRECISION,
    last_message         TEXT NOT NULL DEFAULT '',
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    status_changed_at    TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX monitors_host_idx ON monitors (host_id);

-- Raw results of every check.
CREATE TABLE check_results (
    id         BIGSERIAL PRIMARY KEY,
    monitor_id BIGINT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    checked_at TIMESTAMPTZ NOT NULL,
    status     SMALLINT NOT NULL,
    latency_ms DOUBLE PRECISION,
    message    TEXT NOT NULL DEFAULT '',
    details    JSONB
);
CREATE INDEX check_results_monitor_time_idx ON check_results (monitor_id, checked_at DESC);
CREATE INDEX check_results_time_idx ON check_results (checked_at);

-- Hourly roll-ups, maintained incrementally on every insert. They keep long
-- term charts and uptime figures cheap even with 2 years of history.
CREATE TABLE check_results_hourly (
    monitor_id     BIGINT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    bucket         TIMESTAMPTZ NOT NULL,
    total          INTEGER NOT NULL DEFAULT 0,
    up             INTEGER NOT NULL DEFAULT 0,
    degraded       INTEGER NOT NULL DEFAULT 0,
    down           INTEGER NOT NULL DEFAULT 0,
    latency_sum    DOUBLE PRECISION NOT NULL DEFAULT 0,
    latency_count  INTEGER NOT NULL DEFAULT 0,
    latency_min    DOUBLE PRECISION,
    latency_max    DOUBLE PRECISION,
    PRIMARY KEY (monitor_id, bucket)
);
CREATE INDEX check_results_hourly_bucket_idx ON check_results_hourly (bucket);

-- Status transitions (incidents / recoveries).
CREATE TABLE events (
    id          BIGSERIAL PRIMARY KEY,
    monitor_id  BIGINT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    from_status SMALLINT NOT NULL,
    to_status   SMALLINT NOT NULL,
    message     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX events_monitor_time_idx ON events (monitor_id, created_at DESC);
CREATE INDEX events_time_idx ON events (created_at);

CREATE TABLE status_pages (
    id          BIGSERIAL PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    published   BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE status_page_monitors (
    status_page_id BIGINT NOT NULL REFERENCES status_pages(id) ON DELETE CASCADE,
    monitor_id     BIGINT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    position       INTEGER NOT NULL DEFAULT 0,
    display_name   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (status_page_id, monitor_id)
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT INTO settings (key, value) VALUES
    ('raw_retention_days', '90'),
    ('aggregate_retention_days', '730'),
    ('events_retention_days', '730');
