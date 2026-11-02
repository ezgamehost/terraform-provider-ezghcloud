# Compute and storage lifecycle

The schemas were checked against the public Compute, Block Storage and Images
OpenAPI documents on 2026-11-01. Public responses use UUID `id` plus prefixed
`slug`; Terraform resource IDs use `slug`. UUID operation targets are preserved
until the resource read canonicalizes the identity.

| Resource / field | Behavior |
| --- | --- |
| Every resource `project_id` | Replacement |
| Every resource `name`, `tags` | Update in place |
| SSH `public_key` | Replacement; preserve configured whitespace when API normalization returns the same key material |
| Volume `volume_type` | Replacement; the API describes type changes as unavailable |
| Volume `size_bytes` | Exact decimal string, grow in place, shrinking refused |
| Volume `iops`, `throughput_mbps` | Modify operation in place |
| Volume `deletion_protection` | Update in place; explicit refusal on destroy while enabled |
| Instance `bot_id` | Update in place |
| Instance type, image, SSH keys, user data, root settings, interfaces, launch volume IDs | Replacement |
| Instance status, assigned interfaces, root volume ID and resolved image ID | Computed |

Async operations have a configurable positive `operation_timeout`, default `30m`.
Assigned identity and operation ID are saved before waiting. Failed operations
return errors with recoverable identity. State refresh never treats a hidden or
inaccessible project as a deleted resource. Before deleting independently managed
volumes, Terraform must destroy dependent instances; launch attachments have
`deleteOnTermination=false` in the Compute API.

Import IDs are `organization_id/project_id/resource_id`. Resource IDs are the
canonical `ssh_`, `ins_` and `vol_` IDs. Original instance user data is unreadable,
and original root launch sizing cannot be reconstructed from the instance read.
Imported instances recover their exact image ID and attached interfaces, not the
alias or omitted launch defaults. Match configuration to imported topology before
applying; import cannot recover whether a default firewall or address flag was
explicitly supplied at launch. The `operation_id` output is historical and cannot
be recovered on import. An imported SSH key uses the API's normalized public key.

The instance `volume_ids` input excludes its root volume, which is exposed separately
as `root_volume_id`. `volume_ids` is a launch attachment declaration, so changes
replace the instance; independently managed volumes survive instance destruction.

The singular data sources are `ssh_key`, `volume`, `instance`, `image`,
`instance_type` and `volume_type`. Type lookups use exact names, image lookup uses
`ref` and `project_id`, and resource lookups use `id` and `project_id`.

Default tests use public-API fixtures. Actual Terraform acceptance tests run with
`TF_ACC=1` and `TF_ACC_TERRAFORM_PATH` pointing to Terraform. They create only
fixture infrastructure. Real live infrastructure lifecycle behavior has not been
verified by this package's tests.
