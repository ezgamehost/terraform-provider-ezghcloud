# EZGH Cloud Terraform provider

Status: proposed for implementation review, 2026-11-01.

## Intent

Let customers describe an EZGH Cloud deployment as Terraform configuration, review
changes in a plan, apply them, and import existing infrastructure. The first release
must support a coherent deployment from project through instance and storage.

The owner accepted the initial resource scope and authorized a public
`ezgamehost/terraform-provider-ezghcloud` repository. Implementation details below
are proposed defaults unless explicitly recorded as owner decisions.

## Scope

Resources:

| Resource | Purpose |
| --- | --- |
| `ezghcloud_project` | Organization project, including name and optional folder |
| `ezghcloud_vpc` | VPC address space and metadata |
| `ezghcloud_subnet` | Subnet within a VPC |
| `ezghcloud_firewall` | Firewall metadata and its complete rule collection |
| `ezghcloud_ssh_key` | Uploaded public key and metadata |
| `ezghcloud_instance` | Instance and its launch configuration |
| `ezghcloud_volume` | Independently managed block volume |

Add corresponding singular data sources for existing resources, plus an image
lookup and instance-type and volume-type catalog lookups needed to write a usable
configuration. Lookups use explicit IDs wherever possible. Image aliases resolve
to an exact image ID through the image API. Name-based lookups must detect ambiguity.

The first end-to-end example creates a project, VPC, subnet, firewall, public SSH
key, private instance, and a separately managed empty volume attached at launch.
It requires no public IP or gateway. A separate public-interface example can show
public connectivity using the compute API's existing launch options.

The provider manages customer APIs only. IAM provisioning, object storage, DNS,
other products, standalone attachments, and stop/start/resize orchestration are
later additions. No new service endpoint or production deployment is implied.

## Approaches

1. **Typed Go resources using Terraform Plugin Framework (recommended).** Explicit
   schemas and lifecycle methods fit asynchronous creates, import, and state recovery.
   More code is written for each resource, but Terraform behavior is reviewable.
2. **Generate the whole provider from OpenAPI.** Faster surface coverage, but API
   descriptions alone do not define replacement, ownership, defaults, or timeout
   recovery. Generation can later help produce wire models, not lifecycle policy.
3. **Wrap the CLI.** Reuses commands but introduces process and installation
   dependencies and couples state handling to CLI output. Avoid for the provider.

The owner accepted the recommendation to use Go and the framework in the initial
scope discussion. Detailed lifecycle policy remains subject to this design review.

## Provider configuration and authentication

Use provider name `ezghcloud` and binary `terraform-provider-ezghcloud`.
The intended Registry address is `ezgamehost/ezghcloud`, pending publication under
that namespace. Do not advertise `terraform init` installation until published.

Accept `api_token`, defaulting to `EZGHCLOUD_API_TOKEN`, as a sensitive provider
attribute. Accept `organization_id`, defaulting to `EZGHCLOUD_ORGANIZATION_ID`.
Require both at configuration time when known; diagnose unknown values only when
an operation requires them. Aliased providers support multiple organizations.
Every resource carries its project ID explicitly; projects have no project parent.

Use a customer API or bot credential as a bearer token. Never reinterpret opaque
credentials as JWTs. Do not implement interactive login or depend on CLI credential
storage. Configuration does not cache IAM decisions; each service authorizes calls.

Default to the public HTTPS endpoints for organizations, compute, network,
firewall, block storage, and images. Optional per-service endpoint overrides support
local development and API tests. Never attach credentials to arbitrary redirect
destinations; disable redirects. Do not log authorization, payloads, or user data.

## API client

Keep the HTTP transport and typed API clients in this repository. CLI clients live
under Go `internal` paths and cannot be imported. Follow their established API
semantics without copying unrelated commands or authentication machinery.

Use context deadlines, escaped path components, bounded response bodies, request
IDs and a versioned User-Agent. Preserve structured error codes, request IDs, and
permission names in Terraform diagnostics while excluding request bodies and secrets.
Encode camelCase JSON exactly as each endpoint specifies; model optional and null
values deliberately and preserve exact integer/decimal wire values.

Each logical POST uses one idempotency key across its permitted HTTP retries.
Retry only documented transient responses and safely replayable requests, honoring
`Retry-After`, cancellation, and the resource deadline. Do not retry validation,
authorization, quota, or optimistic-concurrency errors blindly. Use ETag/If-Match
where supported; conflicts require refresh and a new plan, not an unconditional write.

Operation polling is bounded polling of the public operation endpoint, honoring
`Retry-After`. Validate the Location origin and path before authenticated requests;
never follow an operation URL to a different host. No background worker is needed.

## Resource lifecycle and state

Use explicit snake_case Terraform schemas and typed framework models. Set resource
IDs to the API's canonical prefixed IDs; imports include all parent IDs needed to
read independently of defaults. Organization-scoped imports use
`org_id/resource_id`; project-scoped imports use `org_id/project_id/resource_id`.
Imported organization IDs must match the configured provider organization.

Read all configurable values returned by the API. Keep computed timestamps,
status, addresses, and relationship IDs separate from desired configuration.
Normalize server defaults in schemas and plan modifiers so a successful apply
produces an empty subsequent plan. Never suppress material differences with JSON
string comparisons or broad diff suppression.

A refresh removes state only when the exact resource read explicitly confirms
absence, with the service's authorization-hiding semantics considered. A missing
parent, permission failure, unavailable service, or malformed response must not
silently cause resource recreation. Use parent checks when a 404 is ambiguous.
Delete treats confirmed absence as success and surfaces dependency conflicts.

The implementation plan must include a field-level lifecycle matrix checked against
the current OpenAPI and handlers before resource code is written. Mutable metadata
updates in place. Immutable scope, VPC/subnet CIDRs, SSH public key material, image,
and instance launch topology require replacement unless the public API supports the
precise desired update. The first release does not stop an instance automatically
to resize it. Reject unsupported changes explicitly rather than ignoring them.

### Asynchronous operations

Compute and block storage creates return 202. Capture the assigned resource and
operation IDs immediately. Persist recoverable identity before waiting. On timeout,
cancellation, or terminal failure, return diagnostics with those identifiers and
retain enough state to refresh or destroy the created resource. Never repeat a
create merely because its waiter timed out. Resource reads reconcile transitional
state and operation status before attempting another mutation.

Expose configurable create/update/delete timeouts with conservative defaults;
the plan will choose defaults from the services' documented operation behavior.
Deletion waits for the operation's terminal status and confirmed logical deletion.
Do not interpret a transitional state or unknown operation status as success.

### Ownership

Firewall rules are managed as a complete inline collection in the firewall
resource, using the API's replace operation. Represent rules by their semantic
fields, with computed server IDs kept out of desired membership comparisons.
Do not introduce a competing standalone rule resource in this release. An omitted
collection must have a documented ownership meaning; the proposed default is an
explicit empty collection for a newly managed firewall. Import reads the existing
rules into state before a configuration can replace them.

Instance network interfaces are launch configuration. Preserve ordered positions
where the API uses index zero as primary. Support explicit subnet, address flags,
firewall IDs and an existing interface ID according to API constraints. Omitted
firewall IDs and an explicit empty list have different meanings and stay distinct.
Record generated interface IDs as computed output. Existing interfaces and volumes
remain independently owned; attachment never transfers ownership to the instance
resource. Verify actual terminate/detach behavior before supporting that combination.

The compute-created root volume belongs to the instance lifecycle. Do not also
declare it as an independently created Terraform volume. Expose root volume ID and
supported launch sizing. Independently managed volumes initially support empty
sources, type, size, tags, protection, and API-supported performance settings;
restores and image sources can be added after the initial lifecycle is proven.

Keep volume sizes exact: expose integer GiB when representable and convert to the
API's decimal-string byte count with checked arithmetic. Never round imported sizes;
use exact bytes in state if the API permits non-GiB values.

Mark user data sensitive and explain that Terraform state can still contain it.
Do not generate private SSH keys. Do not infer unchanged launch inputs from API
fields that are intentionally unreadable: preserve configured values, and document
import limitations for such fields. Mutable image aliases must not silently change
the image of an existing instance on refresh; resolve once per planned launch.

## Verification

Use `terraform-plugin-testing` for actual Terraform plan/apply/import behavior.
API fixtures may emulate our own public APIs, including delayed operations, but
must follow captured schemas. Cover a small number of meaningful full lifecycles:
create, refresh with empty plan, update, import, drift, replacement, and destroy.
Add focused client/waiter tests for idempotent retry, Retry-After, cancellation,
permission errors, 404 hiding, and state retention after an interrupted create.

Live acceptance tests require an explicit opt-in, a dedicated test organization
and project, narrowly scoped credentials, and unique resource names. Use the dev
CLI profile if the CLI is used for verification or cleanup. Never select the
default CLI profile implicitly. Tests must track created IDs and clean up only
their own resources. Do not run live tests against unrelated customer resources.
No load tests or benchmarks.

Linux CI checks formatting, vet, race tests, build, documentation generation drift,
and the Terraform acceptance suite against API fixtures. Cross-compile release
targets without adding macOS or Windows test jobs. Keep test jobs near ten minutes.
Use only public dependencies so contributors can build from the public repository.

## Documentation and publication

Generate resource and data source references from schemas with `tfplugindocs`.
Maintain runnable examples and a first deployment guide. Explain import formats,
replacement, deletion protection, credentials, state sensitivity, and timeouts.
Use customer vocabulary and omit internal deployment details.

Use versioned GitHub releases with platform binaries, SHA-256 checksums and signed
checksum files required for Registry distribution. Registry onboarding and signing
configuration must be completed before claiming the provider is installable.
The public repository authorization covers creating and pushing source; no secret
signing material is committed. Propose MPL-2.0, matching the framework's ecosystem;
confirm the license choice during design approval before adding a license file.

## Decisions and acceptance

| Decision | Source |
| --- | --- |
| First scope: project, VPC, subnet, firewall, SSH key, instance, volume; import and lookups | Owner accepted initial proposal |
| Public repository under ezgamehost | Owner, 2026-11-01 |
| Go and Terraform Plugin Framework | Accepted initial proposal |
| Remaining details in this document | Proposed implementation defaults |

Design evidence: read-only GETs of the six public `/openapi.json` documents on
2026-11-01 confirm the proposed resource APIs. The initial implementation must
recheck schemas and served behavior. No service API changes have been requested.

The first release is ready when the documented deployment applies, refreshes to
an empty plan, imports without replacement, and destroys in dependency order;
interrupted operations retain recoverable identity; generated reference and
examples agree with the tested schemas; and a published build can be installed
through its documented distribution channel.
