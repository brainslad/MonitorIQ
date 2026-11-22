-- E-mail notifications: per-monitor recipients, per-user opt-in and a
-- log of the messages sent.
ALTER TABLE monitors
    ADD COLUMN alert_emails TEXT NOT NULL DEFAULT '',
    ADD COLUMN alerts_muted BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE users
    ADD COLUMN email_alerts BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE email_log (
    id         BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind       TEXT NOT NULL,
    recipients TEXT NOT NULL,
    subject    TEXT NOT NULL,
    ok         BOOLEAN NOT NULL,
    error      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX email_log_created_idx ON email_log (created_at);
