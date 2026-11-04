# Databases lifecycle

The schemas were checked against databases-api's OpenAPI overlay (databases-stack `2ae8787`) on
2026-11-02. Responses carry a UUID `id` and a prefixed `slug`; Terraform IDs are the slug (`dbi_`,
`dbb_`). Each create answers `202` with an operation whose `target.resourceName` ends in the slug, so
the identity and operation ID are saved before waiting, and a failed operation leaves a refreshable
resource in state.

| Resource / field | Behavior |
| --- | --- |
| Database `project_id`, `engine`, `subnet_id`, `storage_volume_type` | Refused at plan, an unknown value too (a replaced subnet or project): nothing replaces a database, since a replacement destroys its data and restorable range. The error says to create a new database and move the data, or restore into a new one |
| Database `source_*` | Set once at create, never a replacement: removing a restore's source must never destroy the database. A later change or removal is saved to state only, with a plan warning |
| Database `engine_version` | Refused at plan: a replacement would destroy data. Major upgrades go through a blue/green deployment; its switchover keeps the `dbi_`, name and endpoint, so set the new version after it and nothing is replaced |
| Database `storage_size_gib` | Grows in place; shrinking is refused at plan |
| Database `instance_type` | Changes in place, with a plan warning that a blue/green switchover to a new instance type wants the config set to it, not a resize back |
| Database `description` | `""` reads back as `""` (the API shows it as null) |
| Database `name`, `description`, `tags`, `instance_type`, `storage_iops`, `storage_throughput_mbps`, `allowed_source_cidrs`, backup and maintenance windows, `backup_retention_days`, `parameters`, `log_exports`, `deletion_protection` | One `PATCH` in place, sending only what changed beyond the row fields |
| Database `parameters` | Only configured keys are read back, so a default the API adds isn't drift; import reads none. The API takes the whole set, so an update sends the current set with the keys removed from config dropped and the configured ones laid over: keys Terraform never managed survive |
| Database `deletion_protection` | Destroy is refused while true |
| Database `final_backup_on_destroy` | Provider only, default `true`: destroy sends `finalBackup` |
| Database `admin_secret_id` | The Secret holding the admin credentials. State never holds the password |
| Backup `project_id`, `database_id` | Refused at plan, an unknown value too: a replacement would delete the backup, which outlives its database. So `-replace` or a taint of a database a backup names by attribute is refused; a backup that names a literal `dbi_` is kept |
| Backup destroy | `DELETE` answers with the backup deleting; it has left the project's lists |

| Logical database and user `project_id`, `database_id`, `name`; user `logical_database` | Replace the object. Replacing a logical database drops its data |
| User `preset` | One `PATCH` in place, so the user and its password stay |
| User `secret_id` | The Secret holding `username`, `password`, `host`, `port`, `database`, `engine` and `uri`. State never holds the password, and no attribute is sensitive |
| Logical database and user `status` `missing` | Absent from the engine's latest report (dropped in SQL). The plan shows `status` going to `available`, an in-place update: a user's rotates its password, which creates it again with its preset; a logical database's creates it again. Right after an operation succeeds, `missing` reads as `available`, since the report can trail it |
| Logical database and user `failure_code`, `failure_message` | The API's `failure`: why a create failed, or why the engine refused a user's drop (that user stays `available` with `secret_id` null until it's rotated). Null otherwise. An update plans them null, since every operation that succeeds clears them |
| Logical database and user read | A `404`, a row that's `deleting`, or a name made only in SQL removes it from state |
| Logical database and user import | `organization_id/project_id/dbi_…/name`. A name made in SQL is refused: only objects created through EZGH can be imported |

Checked against databases-api's OpenAPI overlay (databases-stack `dc60949`) on 2026-11-03: the
routes, request bodies (`LogicalDatabaseCreate`, `DatabaseUserCreate`, `DatabaseUserUpdate`; rotate
takes no body), the `LogicalDatabase` and `DatabaseUser` fields and their nullability, and `202` with
an operation and `Location` on every change. A create of a managed logical database that reads
`missing` is accepted and makes it again; refusals (`409 already_exists`, `user_failed`,
`database_busy`, `logical_database_not_available`, `quota_exceeded`, …) show the API's message.

API errors add databases-api's message and its extras (`restorableRanges`, `reasons`, `issues`); an
`access_denied` names only the permission, as elsewhere in the provider.

Taint and `-replace` (or `replace_triggered_by`) of a database are explicit and still replace it,
unless a backup names it by attribute. A create whose wait ends (timeout or interrupt) while the
database is still `creating` is a warning, not an error, so it isn't tainted and destroyed by the next
apply; refresh resumes the wait. A failed create is an error and is tainted.

`operation_timeout` defaults to `60m`. Refresh resumes a `creating` database or backup through its saved operation; a `deleting` database
stays in state until it's gone. A `404` is read as gone only after the project read
succeeds. Import IDs are `organization_id/project_id/dbi_…` or `…/dbb_…`; `source_*`, `operation_id`
and `parameters` can't be recovered on import.

Firewall rules take `peer_database_id` (`{"database": "dbi_…"}` on the wire), one peer of three.

`TestLiveDatabaseLifecycle` runs only with `TF_ACC=1` and `EZGH_PROFILE=dev`, plus
`EZGHCLOUD_API_TOKEN`, `EZGHCLOUD_ORGANIZATION_ID`, `EZGHCLOUD_TEST_PROJECT_ID` and
`EZGHCLOUD_TEST_SUBNET_ID`. It checks `ezgh --profile dev orgs list` lists the organization, then
creates one `gs-1.large` database, adds a logical database and a `readWrite` user, checks the user's
Secret exists (`ezgh --profile dev secrets read … --field username`), destroys everything with no
final backup, and checks the Secret is gone.
