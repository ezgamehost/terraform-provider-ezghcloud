# Networking lifecycle

Wire schemas were checked against the served network and firewall OpenAPI documents
on 2026-11-01 and the corresponding target authorization handlers.

| Resource field | Lifecycle |
| --- | --- |
| All resources: project_id | Replace; organization_id is computed from provider and checked on refresh/import |
| All resources: name, description, tags | PATCH in place |
| All resources: id, resource_name, status | Read from service; ID is the prefixed slug |
| VPC: ipv4_cidr | Replace |
| VPC: ipv6_cidr, default_firewall_id | Computed |
| Subnet: vpc_id, ipv4_cidr | Replace |
| Subnet: ipv6_cidr | Computed |
| Subnet: auto_assign_public_ipv4, auto_assign_public_ipv6 | PATCH in place; also set after initial create if enabled |
| Firewall: rules | Authoritative set replacement with If-Match |
| Firewall: etag | Computed version for optimistic concurrency |
| NAT gateway: name | PATCH in place (it has no description or tags) |
| NAT gateway: vpc_id, subnet_id | Replace |
| NAT gateway: public_ipv4_addresses | Computed at create; kept across updates; new on replacement |
| NAT gateway: timeouts | create and delete waits, 20m each by default; changed in place |

Firewall rules use semantic fields only, so generated rule IDs and order do not
cause drift. Omitted rules mean an explicitly empty collection at create. Each
rule has one peer selector, optional TCP/UDP ports or ICMP selectors, and an
optional description defaulting to an empty string. Canonical protocol names are
required for TCP, UDP, ICMP and ICMPv6; other protocols use canonical IANA numbers.

Before replacing rules the provider verifies that the server version still matches
the refreshed state. It refuses replacement when the API omits ETag. Metadata
PATCH follows the API's existing semantics; rule replacement carries If-Match.
Do not manage a firewall's rule collection concurrently with another owner.

Resources import as `organization_id/project_id/resource_id`. A mismatched provider
organization is an error. Resources and lookups use prefixed IDs. The provider does
not create, delete, or adopt the generated default firewall as a separate resource.

A missing resource is removed from state only after its service returned 404 and
the parent project is readable. Project authorization failures preserve state.
This conservative check requires permission to read the parent project. Delete
conflicts preserve state. VPC deletion can return 204 while address withdrawal is
pending, so the provider waits up to ten minutes for confirmed absence and retains
state on interruption or failure.

Read-only lookups exist for VPC, subnet and firewall by exact canonical ID. Fields
outside this table, including explicit subnet route-table association, are not
managed by this first provider surface.

`ezghcloud_nat_gateway`'s wire shape was checked against network-stack's OpenAPI document at
c397dce8. Create and delete are asynchronous and the gateway is its own operation: the provider records it from the
accepted answer, then reads it until it is `active` (create) or until reading it answers 404
(delete), for the `timeouts` attribute's `create` and `delete` (20 minutes each by default). A read
that fails during the wait is tried again, whatever the failure; only the limit, or the gateway
being gone or deleted under a create, ends a wait in an error. A create that times out leaves the
gateway in state, tainted: `terraform untaint` keeps it once it is active. A replaced gateway
(project, VPC or subnet changed, or a tainted one applied) gets a new public IPv4 address.
Routes to a gateway aren't managed: the provider has no route-table resource.
