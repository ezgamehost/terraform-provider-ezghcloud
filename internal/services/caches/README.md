# Caches lifecycle

The schemas were checked against caches-api's OpenAPI document (caches-stack `7593750`, built from
its manifest and overlay) on 2026-11-09. Terraform IDs are the slug (`cch_`); a user's is
`<cch_>/<name>`. A create answers `202` with an operation whose `target.resourceName` ends in the slug,
so the identity and operation ID are saved before waiting. Blue/green deployments stay out of
Terraform, as for databases.

| Resource / field | Behavior |
| --- | --- |
| Cache `project_id`, `engine`, `mode`, `subnet_id` | Replace the cache (a cache is replaceable; databases aren't). A change of mode in place goes through a blue/green deployment outside Terraform |
| Cache `engine_version` | Upgrades in place with its own `PATCH`, after any other change in the same apply (the API takes `engineVersion` alone). An older version is refused at plan: upgrades are one-way |
| Cache `mode`, `storage_size_gib`, `storage_iops`, `storage_throughput_mbps` | Persistent needs storage, memory only refuses any storage setting, Memcached is memory only: checked at plan. A replacement skips the version and storage checks. Storage grows in place; shrinking is refused at plan |
| Cache `plaintext_port` | Memcached only (the API defaults it to true); configuring it on Valkey is refused at plan |
| Cache `parameters` | Only configured keys are sent and read back. The API changes only the keys a `PATCH` names, so removing a key from config leaves its value as it is |
| Cache `name`, `description`, `tags`, `deletion_protection` | Row changes, sent on every update |
| Cache `deletion_protection` | Destroy is refused while true |
| User `project_id`, `cache_id`, `name` | Replace the user (a new password in a new Secret) |
| User `preset` | Valkey only; one `PATCH` in place, so the user and its password stay. Null on Memcached |
| User `secret_id` | The Secret holding `username`, `password`, `host`, `port`, `engine` and, on Valkey, `uri`. No attribute holds the password, and none is sensitive |
| User `default` | Comes with the cache; import it to manage its preset. Destroying it only removes it from state (the API answers `409 default_user`) |
| Cache `engine_version` on Memcached | Applied in the maintenance window, so the apply doesn't wait: it warns, and state keeps the new version while the cache reads `upgrading` |
| Any update | Sends only what changed: during a blue/green deployment caches-api refuses a body that names a setting it can't change then |
