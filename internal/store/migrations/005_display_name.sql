-- display_name_custom: the display name was set in MonitorIQ (by the user
-- or an administrator), so the name reported by LDAP / SSO no longer
-- replaces it.
ALTER TABLE users
    ADD COLUMN display_name_custom BOOLEAN NOT NULL DEFAULT false;
