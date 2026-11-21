-- External identities (LDAP / OpenID Connect), two-factor authentication
-- and partially authenticated sessions.
ALTER TABLE users
    ADD COLUMN auth_source    TEXT NOT NULL DEFAULT 'local' CHECK (auth_source IN ('local', 'ldap', 'oidc')),
    ADD COLUMN external_id    TEXT NOT NULL DEFAULT '',
    ADD COLUMN display_name   TEXT NOT NULL DEFAULT '',
    ADD COLUMN email          TEXT NOT NULL DEFAULT '',
    ADD COLUMN totp_secret    TEXT NOT NULL DEFAULT '',
    ADD COLUMN totp_enabled   BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN totp_last_step BIGINT NOT NULL DEFAULT 0;

CREATE UNIQUE INDEX users_external_idx ON users (auth_source, external_id) WHERE external_id <> '';

CREATE TABLE recovery_codes (
    user_id   BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash BYTEA NOT NULL,
    used_at   TIMESTAMPTZ,
    PRIMARY KEY (user_id, code_hash)
);

-- mfa: '' = fully signed in, 'verify' = waiting for a 2FA code,
-- 'enroll' = 2FA is mandatory and the user must set it up first.
ALTER TABLE sessions
    ADD COLUMN mfa    TEXT NOT NULL DEFAULT '' CHECK (mfa IN ('', 'verify', 'enroll')),
    ADD COLUMN method TEXT NOT NULL DEFAULT 'local';
