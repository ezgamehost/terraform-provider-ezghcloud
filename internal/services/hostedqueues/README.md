# Hosted Queues lifecycle

Served only with `EZGHCLOUD_PREVIEW_HOSTED_QUEUES=1` until Hosted Queues launch (Gate 4), so releases
and their generated docs leave it out. The schemas were checked against hosted-queues-stack's
`openapi.overlay.json` at `cd0b7bb` on 2026-11-14. Terraform IDs are the slug (`hqc_`); a user's is
`<hqc_>/<name>`, a topic's or vhost's `<hqc_>/<name>`, and a RabbitMQ object's `<hqc_>/<vhost>/<name>`
(`default` for `/`; a binding's name is its server-assigned key).

**HC7:** every call names its own object's path. Nothing lists a cluster's objects, so an object no
configuration names (an app's topic, Celery's reply queues) is never read, changed or deleted, and
destroying a resource deletes that object alone. Destroying a vhost that still holds queues is refused
by the API; the provider never passes `force`.

| Resource / field | Behavior |
| --- | --- |
| Cluster `project_id`, `engine`, `subnet_id` | Replace the cluster |
| Cluster `mode` | `single` grows to `replicated` in a `PATCH` of its own (with `raiseAppTopics` on Kafka, from `raise_app_topics`); back to `single` is refused at plan |
| Cluster `engine_version` | Upgrades in place with its own `PATCH`, after growth; an older version is refused at plan |
| Cluster `storage_size_gib` | Grows in place; shrinking is refused at plan |
| Cluster `settings`, `unauthenticated_access` | Kafka only (refused at plan on RabbitMQ). Only changed keys are sent; a key left out keeps its value |
| Cluster other settings | Sent only when changed, in one `PATCH` before growth and an upgrade |
| User `project_id`, `cluster_id`, `name` | Replace the user (a new password in a new Secret) |
| User `preset`, `topics`, `groups` (Kafka), `vhosts` (RabbitMQ) | One `PATCH {permissions}` in place, so the user and its password stay. A vhost written `default` stays so in state (the API shows `/`) |
| User `secret_id` | The Secret holding the password and connection details. No attribute holds the password, and none is sensitive |
| User `app` | Comes with the cluster; import it to manage it. It can be destroyed like any other |
| Topic `partitions`, `replication_factor` | Raised in place; lower is refused at plan |
| Topic `config` | Only configured keys are read back; removing one sends `null`, the cluster's default |
| Vhost `default_queue_type`, `max_queues`, `max_connections` | In place; a null limit is no limit |
| Queue, exchange, binding | Any change replaces them (the API has no update) |
| Policy `pattern`, `apply_to`, `priority`, `definition` | In place |
| `arguments`, `definition` | JSON objects (`jsonencode`); state keeps the configured text while it means the same |

Not managed: `raise-replication` (a one-off action), consumer groups (apps make them), queue purges.

At Gate 4: drop `Preview`, add `hostedqueues` to the provider's endpoints description, add examples,
run `make docs`, and run `TestLiveHostedQueuesLifecycle` (Gates 2/3) with `TF_ACC=1 EZGH_PROFILE=dev`.
