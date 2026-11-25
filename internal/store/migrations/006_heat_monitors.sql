-- heat_monitors: the monitors a user pinned to the dashboard availability
-- map (at most 10, in order); the rest of the map is filled at random.
ALTER TABLE users
    ADD COLUMN heat_monitors BIGINT[] NOT NULL DEFAULT '{}';
