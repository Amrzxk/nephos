-- A replay must return the original create response even if reconciliation changes
-- state or the resource is deleted before the 24-hour key expires.
ALTER TABLE idempotency_requests
    ADD COLUMN response_json TEXT NOT NULL DEFAULT '';

INSERT INTO schema_migrations(version, applied_at) VALUES (2, unixepoch());
