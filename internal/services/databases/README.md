# Databases lifecycle

The schemas were checked against databases-api's OpenAPI overlay (databases-stack `2ae8787`) on
2026-11-02. Responses carry a UUID `id` and a prefixed `slug`; Terraform IDs are the slug (`dbi_`,
`dbb_`). Each create answers `202` with an operation whose `target.resourceName` ends in the slug, so
the identity and operation ID are saved before waiting, and a failed operation leaves a refreshable
resource in state.

| Resource / field | Behavior |
| --- | --- |
| Database `project_id`, `engine`, `subnet_id`, `storage_volume_type`, `source_*` | Replacement |
| Database `engine_version` | Refused at plan: a replacement would destroy data. Major upgrades go through a blue/green deployment; after its switchover, set the new version |
| Database `storage_size_gib` | Grows in place; shrinking is refused at plan |
| Database `name`, `description`, `tags`, `instance_type`, `storage_iops`, `storage_throughput_mbps`, `allowed_source_cidrs`, backup and maintenance windows, `backup_retention_days`, `parameters`, `log_exports`, `deletion_protection` | One `PATCH` in place, sending only what changed beyond the row fields |
| Database `parameters` | The whole set. Only configured keys are read back, so a default the API adds isn't drift; import reads none |
| Database `deletion_protection` | Destroy is refused while true |
| Database `final_backup_on_destroy` | Provider only, default `true`: destroy sends `finalBackup` |
| Database `admin_secret_id` | The Secret holding the admin credentials. State never holds the password |
| Backup `project_id`, `database_id` | Replacement (a new backup) |
| Backup destroy | `DELETE` answers with the backup deleting; it has left the project's lists |

`operation_timeout` defaults to `60m`. Refresh resumes a `creating` or `deleting` database (and a
`creating` backup) through its saved operation. A `404` is read as gone only after the project read
succeeds. Import IDs are `organization_id/project_id/dbi_…` or `…/dbb_…`; `source_*`, `operation_id`
and `parameters` can't be recovered on import.

Firewall rules take `peer_database_id` (`{"database": "dbi_…"}` on the wire), one peer of three.

`TestLiveDatabaseLifecycle` runs only with `TF_ACC=1` and `EZGH_PROFILE=dev`, plus
`EZGHCLOUD_API_TOKEN`, `EZGHCLOUD_ORGANIZATION_ID`, `EZGHCLOUD_TEST_PROJECT_ID` and
`EZGHCLOUD_TEST_SUBNET_ID`. It checks `ezgh --profile dev orgs list` lists the organization, then
creates and destroys one `gs-1.large` database with no final backup.
