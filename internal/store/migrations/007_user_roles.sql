-- role: what a user may do. 'admin' manages everything (monitors, hosts,
-- groups, status pages, users and settings); 'viewer' only looks at the
-- dashboard, monitors and reports and manages their own account.
-- Existing accounts stay administrators; new ones are viewers unless an
-- administrator chooses otherwise.
ALTER TABLE users
    ADD COLUMN role TEXT NOT NULL DEFAULT 'admin' CHECK (role IN ('admin', 'viewer'));
ALTER TABLE users ALTER COLUMN role SET DEFAULT 'viewer';
