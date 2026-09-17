# ADR-0008: REST + OpenAPI API, not AWS wire compatible

- **Status:** Accepted
- **Date:** 2026-09-17

## Context

The CLI, the web console, the lab engine, and later the Terraform provider all consume the Nephos API. The API should be approachable with `curl`, reasonably stable, and should transfer AWS skills. It is explicitly **not** a goal to accept AWS SDK calls.

## Options considered

### Option A: AWS wire compatibility (Query/JSON protocols, SigV4 signing)
- Pros: existing AWS tools would work.
- Cons:
  - A bottomless pit of protocol quirks.
  - Forces AWS API warts into every surface.
  - Competes with testing emulators on their home turf instead of on learning.

### Option B: gRPC (optionally with grpc-gateway)
- Pros: typed contracts and streaming.
- Cons: browsers need a gateway; not `curl`-friendly; heavier for learners reading the API.

### Option C: ConnectRPC
- Pros: protobuf schemas; works in browsers; JSON over HTTP.
- Cons: RPC-style URLs are less familiar than resources, and the tooling is less universal than OpenAPI.

### Option D: REST/JSON described by an OpenAPI 3.1 specification, spec-first
- Pros:
  - `curl`-friendly; universal tooling.
  - Generated Go server, Go client, and TypeScript types.
  - Maps naturally onto Terraform's create/read/update/delete lifecycle.
- Cons: code-generation friction; less rigid than protobuf.

## Decision

Use **REST/JSON, spec-first with OpenAPI 3.1**. `api/openapi.yaml` is the source of truth.

**Generated code**
- Go server interfaces and types (`oapi-codegen`).
- A public Go client (`pkg/client`) shared by the CLI and the Terraform provider.
- TypeScript types for the console.
- CI fails if generated code is stale.

**Paths**
- `/v1/workspaces/{workspace}/<resources>/{id}`, e.g. `/v1/workspaces/default/vpcs/vpc-0a1b2c3d4e5f67890`.
- Global endpoints: `/v1/events` (server-sent events), `/v1/health`, `/v1/version`.

**IDs**
- AWS-style prefix plus 17 lowercase hex characters, stable forever.
- Prefixes: `vpc-`, `subnet-`, `i-`, `eni-`, `sg-`, `sgr-`, `acl-`, `rtb-`, `rtbassoc-`, `igw-`, `nat-`, `eipalloc-`, `key-`, `ami-`.

**Names**
- A `name` field mirrors the AWS `Name` tag.
- Deviation from AWS: names are **unique per resource type within a workspace**, so the CLI and lab files can refer to resources by name.
- Arbitrary `tags` are also supported.

**Field names**
- snake_case, matching Terraform AWS provider attribute names wherever the concept maps one-to-one.
- Examples: `cidr_block`, `availability_zone`, `map_public_ip_on_launch`, `vpc_security_group_ids`, `associate_public_ip_address`, `user_data`, `from_port`, `to_port`, `cidr_blocks`.

**Asynchronous lifecycle**
- Create calls return the resource in a transitional state.
- Instance states match EC2: `pending`, `running`, `stopping`, `stopped`, `shutting-down`, `terminated`.
- NAT gateway states: `pending`, `available`, `deleting`, `deleted`, `failed`.
- Clients poll with `GET` or subscribe to `/v1/events`. The CLI offers `--wait`.

**Idempotency**
- Create calls accept an `Idempotency-Key` header (the analog of AWS's `ClientToken`).
- A retry with the same key returns the original result for 24 hours.

**Errors**
- HTTP status plus `{"code": "...", "message": "...", "resource_id": "...", "docs_url": "..."}`.
- Codes borrow EC2 names where learners will meet them on AWS: `InvalidVpcID.NotFound`, `InvalidSubnet.Range`, `InvalidSubnet.Conflict`, `DependencyViolation`, `InvalidParameterValue`, `AddressLimitExceeded`, `InsufficientInstanceCapacity`.

**Lists**
- Paginated with `limit` and `page_token`.
- Filterable by common fields, e.g. `?vpc_id=`.

**Authentication**
- A bearer token for the CLI.
- An HttpOnly session cookie for the console (see [ARCHITECTURE](../ARCHITECTURE.md), security model).

**Stability**
- `/v1` may change incompatibly before Nephos 1.0.
- Every change is recorded in the changelog, and the Terraform provider pins supported API versions.

## Consequences

- Positive:
  - A clean, teachable API.
  - Learners who read the JSON see the same field names they will later write in Terraform for AWS.
  - One contract feeds every client.
- Negative / costs:
  - Existing AWS tools (AWS CLI, boto3, the AWS Terraform provider) won't work against Nephos. This is intentional and stated in [VISION](../VISION.md).
  - Keeping names aligned with the Terraform AWS provider requires review discipline.
- Follow-ups: M1 creates the spec skeleton; M9 validates the API against the Terraform plugin framework.

## Validation

- Every example in the docs is runnable with `curl`.
- **Revisit** if the M9 Terraform provider can't implement CRUD for a resource without breaking API changes.
