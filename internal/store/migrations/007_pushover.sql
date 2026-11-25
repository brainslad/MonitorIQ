-- Push notifications through Pushover.
-- Per monitor: whether alerts go to the default Pushover key (on for existing
-- monitors, so turning Pushover on keeps sending everything), and extra user
-- or group keys for this monitor only.
ALTER TABLE monitors
    ADD COLUMN pushover BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN pushover_keys TEXT NOT NULL DEFAULT '';

-- Push messages share the e-mail log; channel tells them apart.
ALTER TABLE email_log
    ADD COLUMN channel TEXT NOT NULL DEFAULT 'email';
CREATE INDEX email_log_channel_idx ON email_log (channel, created_at);
