-- name: InsertVPC :exec
INSERT INTO vpcs (
    id, workspace_id, short_index, name, cidr_block, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetVPC :one
SELECT * FROM vpcs WHERE id = ? AND workspace_id = ?;

-- name: GetVPCByName :one
SELECT * FROM vpcs WHERE workspace_id = ? AND name = ?;

-- name: ListVPCPage :many
SELECT * FROM vpcs
WHERE workspace_id = ? AND id > ?
ORDER BY id
LIMIT ?;

-- name: UpdateVPCStatus :exec
UPDATE vpcs
SET state = ?, state_reason = ?, observed_generation = ?, updated_at = ?
WHERE id = ? AND workspace_id = ?;

-- name: DeleteVPC :exec
DELETE FROM vpcs WHERE id = ? AND workspace_id = ?;

-- name: InsertSubnet :exec
INSERT INTO subnets (
    id, workspace_id, vpc_id, short_index, name, cidr_block,
    availability_zone, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSubnet :one
SELECT * FROM subnets WHERE id = ? AND workspace_id = ?;

-- name: GetSubnetByName :one
SELECT * FROM subnets WHERE workspace_id = ? AND name = ?;

-- name: ListSubnetPage :many
SELECT * FROM subnets
WHERE workspace_id = ? AND id > ?
ORDER BY id
LIMIT ?;

-- name: ListSubnetsByVPC :many
SELECT * FROM subnets WHERE vpc_id = ? ORDER BY id;

-- name: UpdateSubnetStatus :exec
UPDATE subnets
SET state = ?, state_reason = ?, observed_generation = ?, updated_at = ?
WHERE id = ? AND workspace_id = ?;

-- name: DeleteSubnet :exec
DELETE FROM subnets WHERE id = ? AND workspace_id = ?;

-- name: InsertEvent :exec
INSERT INTO events (
    workspace_id, resource_type, resource_id, action, state, generation,
    created_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: EventsAfter :many
SELECT * FROM events WHERE id > ? ORDER BY id LIMIT ?;
