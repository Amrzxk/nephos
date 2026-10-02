-- A cache miss must not replace a root that has ever been observed running.
ALTER TABLE instances ADD COLUMN provisioned INTEGER NOT NULL DEFAULT 0
    CHECK (provisioned IN (0, 1));
UPDATE instances SET provisioned = 1
WHERE state = 'running' OR observed_generation > 0;
INSERT INTO schema_migrations(version, applied_at) VALUES (5, unixepoch());
