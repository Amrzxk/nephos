-- M2: instances choose an instance type. Existing rows were all t3.micro.
ALTER TABLE instances ADD COLUMN instance_type TEXT NOT NULL DEFAULT 't3.micro'
    CHECK (instance_type IN ('t3.nano', 't3.micro', 't3.small', 't3.medium'));
INSERT INTO schema_migrations(version, applied_at) VALUES (6, unixepoch());
