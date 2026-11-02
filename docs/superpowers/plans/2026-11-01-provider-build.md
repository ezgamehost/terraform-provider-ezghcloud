# EZGH Cloud Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Implement the approved infrastructure and IAM provider with import, lookups, examples and executable lifecycle verification.

**Architecture:** A Go Plugin Framework provider configures a shared HTTP client. Independent IAM, network, and compute/storage packages own explicit resource schemas and lifecycle behavior. Public API schemas determine wire types.

**Tech Stack:** Go 1.27, Terraform Plugin Framework, terraform-plugin-testing, tfplugindocs.

**Spec:** ../specs/2026-11-01-terraform-provider-design.md

## Global Constraints

- Public dependencies only; customer APIs only; sensitive credentials and user data.
- Preserve idempotency across retries and IDs across asynchronous failures.
- No implicit account creation, invitation acceptance, credential rotation, or authorization caches.
- Linux CI; cross-platform builds; no load tests; live CLI verification uses the dev profile.
- Commit and push verified chunks on main under the workspace workflow.

## Review Focus

- Ambiguous 404s must not delete Terraform state after a permission change.
- Interrupted asynchronous creates must retain resource identity.
- One-time key secrets must survive refresh and never leak through diagnostics.
- Membership changes must preserve other principals' relationships.
- Imports and API defaults must produce stable subsequent plans.

## Shared interfaces and files

`internal/client/client.go`: `Client{OrganizationID string}` and `Do(ctx context.Context, service, method, path string, body any) (*Response,error)`; `Response{Body []byte, Header http.Header, StatusCode int}` with `Decode(any) error`; `Error{StatusCode int, Code, Message, RequestID string}`; `Wait(ctx, service, location string) error`. Services: orgs, compute, network, firewall, blockstore, images. `ProjectPath(org,project,collection string,ids ...string) string` and `OrgPath(org,collection string,ids ...string) string` escape each component. `List(ctx,service,path string) ([]map[string]any,error)` follows signed cursors.

`internal/services/{iam,network,compute}` each export `Resources() []func() resource.Resource` and `DataSources() []func() datasource.DataSource`. Configure receives `*client.Client` as framework ProviderData. Each team owns only its package and its examples; shared interface changes go through the main implementer.

### Task 1: Shared provider and transport

- [x] Write client tests for stable POST retry keys, denied redirects, paginated lists, safe errors and deadline cancellation; observe failures.
- [x] Implement the shared interface, provider configuration, binary entry point and client tests.
- [x] Verify `go test -race ./internal/client/...`, then commit.

### Task 2: IAM

- [x] Inspect served IAM schemas and handlers; record lifecycle choices in package documentation.
- [x] Write failing tests for bot/group/policy CRUD, relationship isolation, import and sensitive key refresh.
- [x] Implement explicit resources and corresponding lookups, federation, invitations and examples.
- [x] Run package tests and framework schema validation; commit owned paths.

### Task 3: Networking

- [x] Inspect VPC, subnet and firewall schemas; record field lifecycle matrix.
- [x] Write failing API lifecycle/import tests, including CIDR replacement and firewall rules ownership.
- [x] Implement VPC, subnet and firewall resources and singular lookups, with examples.
- [x] Run package tests and schema validation; commit owned paths.

### Task 4: Compute and storage

- [x] Inspect compute, SSH key, volume and image APIs; record lifecycle matrix.
- [x] Write failing tests for operation timeouts retaining IDs, image resolution, import and exact sizes.
- [x] Implement instance, SSH key and volume resources, catalog/image/resource lookups and examples.
- [x] Run package tests and schema validation; commit owned paths.

### Task 5: Integration and distribution

- [x] Register every package and run real Terraform/OpenTofu protocol schema and lifecycle checks against API fixtures.
- [x] Add generated docs, deployment/IAM examples, MPL-2.0, Linux CI and cross-platform release configuration.
- [x] Run formatting, vet, race tests, builds, schema/documentation checks and examples validation.
- [x] Review the final repository for defects, fix concrete findings, commit and push; report any unverified live behavior or Registry setup.

## Execution record

Owner's “build it” authorizes execution. Teams follow workspace path ownership rather than per-task review gates. Opus is not available in this harness; reviews use available agents without claiming an Opus review.

Final verification: Terraform 1.17.1 fixture acceptance and race tests pass for all packages; vet, formatting, generated docs, example validation, and six cross-platform builds pass. Final defect review findings were fixed with regression tests. Live EZGH Cloud resource acceptance and Registry publication remain unverified; signing configuration is required before publishing.
