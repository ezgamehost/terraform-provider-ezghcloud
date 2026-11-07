# Kubernetes lifecycle

The schemas were checked against kubernetes-api's OpenAPI overlay and handlers (kubernetes-stack
`d8d559e`, K1) on 2026-11-06. Responses carry a UUID `id` and a prefixed `slug`; Terraform IDs are the slug
(`kcl_`, `kng_`, `kad_`, `kae_`, `kpa_`). Clusters, node groups and add-ons are asynchronous: a create
answers `202` with an operation whose `target.resourceName` ends in the slug, so the identity and operation
ID are saved before waiting, and a failed operation leaves a refreshable resource in state. A wait cut short
by `operation_timeout` (45m clusters, 30m node groups, 10m add-ons) is a warning, not an error that would
taint something still being made; a cluster's next refresh waits for its create. Access entries and pod
identity associations are synchronous. A destroy that meets a delete in progress waits for it.

| Resource / field | Behavior |
| --- | --- |
| Cluster `subnet_id`, `bot_id`, `encryption_key_id`, `service_ipv4_cidr`, `bootstrap_creator_access` | Refused at plan after create, an unknown value too: replacing a cluster loses its workloads, and its delete refuses while node groups exist anyway |
| Cluster `project_id`, `name` | Replace |
| Cluster `version` | Upgrades in place, one minor at a time; a skip or a downgrade is refused at plan. A `409 precheck_failed` lists each reason (node groups too far behind, APIs removed in the target) |
| Cluster `public_access_cidrs`, `logging_types`, `logging_project_id`, maintenance window, `tags` | One `PATCH` with what changed. `public_access_cidrs = []` closes the public endpoint; left out at create, the API's `0.0.0.0/0`. Removing `public_access_cidrs` or the maintenance window from config later keeps the last value (they're optional and computed); set them to change them |
| Cluster `bootstrap_creator_access` on import | The API doesn't show it, so an import leaves it null and the next apply records the config's value without a change |
| Cluster, node group and add-on read while `creating` or `updating` | When state holds the operation (an apply whose wait was cut short), Read waits for it, so the next plan sees where it ended instead of diffing against a change still running |
| Node group and add-on `status` `failed` | The plan replaces it: delete, then create again, which shows the API's refusal if there is one. A failed `vpc-cni`, `kube-proxy` or `coredns` (not removable) can't be replaced: the plan warns and leaves it, and changes to it are refused until it's active again |
| Node group `desired_size` | Create only, never drift: Cluster Autoscaler owns the size after create. A later change is saved to state with a plan warning. Import reads the current size |
| Node group `min_size`, `max_size`, `version`, `labels`, `taints`, `max_unavailable_percent`, `tags` | One `PATCH` in place |
| Node group `cluster_id`, `name`, `instance_type`, `subnet_id`, `root_volume_size_gib`, `bot_id`, `ssh_key_ids` | Replace |
| Add-on `version` | The API has `version` and `pinned`; here a set `version` pins it (`pinned: true`), and leaving it out follows the cluster (`pinned: false`, the plan's "null follows the cluster"). `installed_version` is what runs |
| Add-on `configuration` | A JSON object string, compared as JSON, so formatting isn't drift. Left out, none (`null`) |
| Add-on a new cluster installs (`vpc-cni`, `kube-proxy`, `coredns`, `pod-identity-agent`, `blockstore-csi`, from kubernetes-stack `addons/*/meta.json`) | Create takes it over (`409 addon_exists` → `PATCH`). Destroy of one that can't be removed (`409 addon_not_removable`) leaves it installed, as last configured, with a warning |
| Access entry `principal_type`, `principal_id` | Two attributes, not the plan's single `principal`: the API takes `{type, id}`, with a user's own ID, a Bot's or SSO user's UUID (never its `bot_` or `usr_` slug) and a `grp_` ID for a group. Nothing is validated here; the API refuses a wrong one. Both replace |
| Access entry `access_policy` | Blocks of `policy` and `namespaces`; left out (or `[]`), the whole cluster (the API's `null`). `clusterAdmin` with namespaces is refused at validate, as the API refuses it |
| Provider `endpoints.kubernetes` | An override like any other service's |
| Pod identity association `namespace`, `service_account`, `cluster_id` | Replace; `bot_id` and `tags` change in place |
| Data source `ezghcloud_kubernetes_cluster` | By `id` and `project_id`: `endpoint_hostname` and `certificate_authority` for the kubernetes provider. No token data source: it would put a bearer in state; `examples/kubernetes/main.tf` uses the `ezgh kubernetes token` exec plugin |

Imports: `organization_id/project_id/kcl_…` for a cluster, `organization_id/project_id/kcl_…/<id>` for the
rest.

`TestLiveKubernetesLifecycle` runs only with `TF_ACC=1` and `EZGH_PROFILE=dev`; it hasn't run yet.
