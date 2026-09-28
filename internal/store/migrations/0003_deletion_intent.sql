-- A failed teardown still needs to remember that the desired state is absent.
ALTER TABLE vpcs ADD COLUMN deletion_requested INTEGER NOT NULL DEFAULT 0
    CHECK (deletion_requested IN (0, 1));
ALTER TABLE subnets ADD COLUMN deletion_requested INTEGER NOT NULL DEFAULT 0
    CHECK (deletion_requested IN (0, 1));
UPDATE vpcs SET deletion_requested = 1 WHERE state = 'deleting';
UPDATE subnets SET deletion_requested = 1 WHERE state = 'deleting';
INSERT INTO schema_migrations(version, applied_at) VALUES (3, unixepoch());
