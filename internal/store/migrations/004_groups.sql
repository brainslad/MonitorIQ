-- Monitor groups: a monitor belongs to at most one group. Deleting a group
-- keeps its monitors, which become ungrouped.
CREATE TABLE monitor_groups (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    color       TEXT NOT NULL DEFAULT 'steel',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX monitor_groups_name_idx ON monitor_groups (lower(name));

ALTER TABLE monitors
    ADD COLUMN group_id BIGINT REFERENCES monitor_groups(id) ON DELETE SET NULL;
CREATE INDEX monitors_group_idx ON monitors (group_id);
