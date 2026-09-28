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
    message, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: EventsAfter :many
SELECT * FROM events WHERE id > ? ORDER BY id LIMIT ?;

-- name: GetIdempotency :one
SELECT * FROM idempotency_requests
WHERE workspace_id = ? AND operation = ? AND key = ?;

-- name: PutIdempotency :exec
INSERT INTO idempotency_requests (
    workspace_id, operation, key, payload_hash, resource_id, response_json, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(workspace_id, operation, key) DO UPDATE SET
    payload_hash = excluded.payload_hash,
    resource_id = excluded.resource_id,
    response_json = excluded.response_json,
    created_at = excluded.created_at,
    expires_at = excluded.expires_at;

-- name: MarkVPCDeleting :exec
UPDATE vpcs SET state = 'deleting', state_reason = '', deletion_requested = 1,
    generation = generation + 1, updated_at = ?
WHERE id = ? AND workspace_id = ? AND deletion_requested = 0;

-- name: MarkSubnetDeleting :exec
UPDATE subnets SET state = 'deleting', state_reason = '', deletion_requested = 1,
    generation = generation + 1, updated_at = ?
WHERE id = ? AND workspace_id = ? AND deletion_requested = 0;

-- name: CountInstancesInSubnet :one
SELECT COUNT(*) FROM instances WHERE subnet_id = ?;
