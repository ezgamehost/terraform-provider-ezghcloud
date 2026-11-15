# Warehouse lifecycle

The schemas were checked against warehouse-stack's OpenAPI at `8eb8791` (every route) on 2026-11-15.
Served only with `EZGHCLOUD_PREVIEW_WAREHOUSE=1` until Warehouse launches (Gate 4). Terraform IDs are
the slug (`whs_`); a user's and a database's are `<whs_>/<name>`.

**Terraform never drops a database it didn't declare.** The warehouse resource never calls a
database route, and reads only its own fields and the `app` user's Secret ID. Only destroying an
`ezghcloud_warehouse_database` deletes a database, that one alone, with its name in the body; the API
refuses it while the warehouse's `deletion_protection` is on. A database that already exists is an
error naming `terraform import`, never adopted.

| Resource / field | Behavior |
| --- | --- |
| Warehouse `project_id`, `subnet_id`, `storage_kind`, `from_backup` | Replace the warehouse |
| Warehouse `shards`, `replicas` | Grow in place, in a `PATCH` of their own (with `keeper` when growing from one node); fewer is refused at plan |
| Warehouse `keeper` | Null for 1 × 1; switches in place in a `PATCH` of its own. Left unset, it's unknown when the shape changes (the API picks `dedicated`) |
| Warehouse `engine_version` | Upgrades in place with its own `PATCH`, last; an older version is refused at plan |
| Warehouse `storage_size_gib` | Grows in place; shrinking is refused at plan. `storage_iops` and `storage_throughput_mbps` are set at create |
| Warehouse other settings | Sent only when changed, in one `PATCH` first |
| Warehouse `app_secret_id` | The `app` user's Secret, read once |
| User `project_id`, `warehouse_id`, `name` | Replace the user (a new password in a new Secret) |
| User `preset`, `grants`, `settings_profile` | One `PATCH` in place, so the user and its password stay |
| User `secret_id` | The Secret holding the password and connection details. No attribute holds the password, and none is sensitive |
| User `app` | Comes with the warehouse; import it to manage it |
| Database every attribute | Replaces it. Names are exact and refused at plan as the API refuses them |
| Database read | `GET` of that database; one dropped outside Terraform leaves the state and is created again |
| Connection every configured attribute | Replaces it (there is no update route). Each type's fields are refused at plan on another type |
| Connection `secret_id` | The Kafka or database user's Secret; null for a bucket |
| Connection `status` | `broken` when its target is gone: it stays in state until destroyed |

Not yet: an `ezghcloud_warehouse_databases` data source over the schema inventory.

`TestLiveWarehouseLifecycle` runs against real EZGH only with `TF_ACC=1 EZGH_PROFILE=dev`, plus
`EZGHCLOUD_API_TOKEN`, `EZGHCLOUD_ORGANIZATION_ID`, `EZGHCLOUD_TEST_PROJECT_ID` and
`EZGHCLOUD_TEST_SUBNET_ID`.
