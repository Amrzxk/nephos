-- M1 state is authoritative. Kernel objects and Podman containers are caches.
-- This migration runs once inside the same transaction as PRAGMA user_version=1.
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);
INSERT INTO workspaces(id) VALUES ('default');

-- AUTOINCREMENT prevents a short kernel name from being reused after deletion.
-- These rows are allocation history, not a second resource source of truth.
CREATE TABLE kernel_indexes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_kind TEXT NOT NULL,
    resource_id TEXT NOT NULL UNIQUE,
    allocated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE vpcs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    short_index INTEGER NOT NULL UNIQUE REFERENCES kernel_indexes(id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    cidr_block TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 1,
    observed_generation INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'available', 'deleting', 'failed')),
    state_reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (workspace_id, name),
    UNIQUE (id, workspace_id),
    CHECK (generation >= 1 AND observed_generation >= 0 AND observed_generation <= generation)
);
CREATE INDEX vpcs_workspace_id_order ON vpcs(workspace_id, id);

CREATE TABLE subnets (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    vpc_id TEXT NOT NULL,
    short_index INTEGER NOT NULL UNIQUE REFERENCES kernel_indexes(id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    cidr_block TEXT NOT NULL,
    availability_zone TEXT NOT NULL
        CHECK (availability_zone IN ('local-1a', 'local-1b', 'local-1c')),
    generation INTEGER NOT NULL DEFAULT 1,
    observed_generation INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'available', 'deleting', 'failed')),
    state_reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (workspace_id, name),
    UNIQUE (id, workspace_id),
    FOREIGN KEY (vpc_id, workspace_id) REFERENCES vpcs(id, workspace_id) ON DELETE RESTRICT,
    CHECK (generation >= 1 AND observed_generation >= 0 AND observed_generation <= generation)
);
CREATE INDEX subnets_vpc_id_order ON subnets(vpc_id, id);
CREATE INDEX subnets_workspace_id_order ON subnets(workspace_id, id);

-- M1's next slice fills these tables. Defining them in version 1 pins the
-- resource relationships before the daemon starts writing network state.
CREATE TABLE instances (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    subnet_id TEXT NOT NULL,
    short_index INTEGER NOT NULL UNIQUE REFERENCES kernel_indexes(id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    generation INTEGER NOT NULL DEFAULT 1,
    observed_generation INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'stopping', 'stopped',
                         'shutting-down', 'terminated', 'failed')),
    state_reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (workspace_id, name),
    UNIQUE (id, workspace_id),
    FOREIGN KEY (subnet_id, workspace_id) REFERENCES subnets(id, workspace_id) ON DELETE RESTRICT,
    CHECK (generation >= 1 AND observed_generation >= 0 AND observed_generation <= generation)
);
CREATE INDEX instances_subnet_id_order ON instances(subnet_id, id);

CREATE TABLE enis (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    instance_id TEXT NOT NULL,
    subnet_id TEXT NOT NULL,
    short_index INTEGER NOT NULL UNIQUE REFERENCES kernel_indexes(id) ON DELETE RESTRICT,
    private_ip TEXT NOT NULL,
    mac_address TEXT NOT NULL UNIQUE,
    generation INTEGER NOT NULL DEFAULT 1,
    observed_generation INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'pending',
    state_reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (subnet_id, private_ip),
    FOREIGN KEY (instance_id, workspace_id) REFERENCES instances(id, workspace_id) ON DELETE RESTRICT,
    FOREIGN KEY (subnet_id, workspace_id) REFERENCES subnets(id, workspace_id) ON DELETE RESTRICT,
    CHECK (generation >= 1 AND observed_generation >= 0 AND observed_generation <= generation)
);
CREATE INDEX enis_instance_id_order ON enis(instance_id, id);

-- Events remain after their resources are deleted, so resource_id is not
-- foreign-keyed to a live resource row. The integer ID is the SSE resume key.
CREATE TABLE events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    action TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT '',
    generation INTEGER NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX events_workspace_id_order ON events(workspace_id, id);

CREATE TABLE idempotency_requests (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    operation TEXT NOT NULL,
    key TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, operation, key)
);
CREATE INDEX idempotency_expiry ON idempotency_requests(expires_at);

INSERT INTO schema_migrations(version, applied_at) VALUES (1, unixepoch());
