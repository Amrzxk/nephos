-- Add observed runtime identity and durable deletion intent without rewriting v1.
ALTER TABLE instances ADD COLUMN deletion_requested INTEGER NOT NULL DEFAULT 0
    CHECK (deletion_requested IN (0, 1));
ALTER TABLE instances ADD COLUMN runtime_id TEXT;
-- M1 has exactly one primary ENI; this makes snapshot reads unambiguous.
CREATE UNIQUE INDEX enis_primary_instance ON enis(instance_id);
CREATE INDEX instances_workspace_id_order ON instances(workspace_id, id);
INSERT INTO schema_migrations(version, applied_at) VALUES (4, unixepoch());
