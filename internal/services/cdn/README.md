# CDN lifecycle

The schema was checked against cdn-api's OpenAPI document (cdn-stack `5d55d45`, built from its
manifest and overlay) on 2026-11-09. Terraform IDs are the slug (`cdn_`).

| Resource / field | Behavior |
| --- | --- |
| `project_id` | Replaces the distribution (a new default hostname; the old one is never reused) |
| Everything else | One whole-document `PUT`, since cdn-api resets an omitted field to its default |
| Updates | Conditional: `If-Match` is the ETag of the last read, kept in private state (the document carries no version). A `412` rereads it once: when what this resource manages is unchanged (only `waf` moved, as `ezghcloud_cdn_distribution_waf`'s replace does), the update is retried at the new ETag; otherwise it asks to refresh and plan again |
| `origin.port` | Filled at plan from `origin.protocol` (443 or 80) when left out |
| `origin.protocol`, `origin.host_header`, `origin.port`, `domain` | Checked at plan: https or http, viewer or origin, 1–65535, at most 10 domains in lowercase without a trailing dot, each once, with a `crt_` certificate. The cover rule, EZGH's own domains and a hostname another distribution holds are cdn-api's to refuse |
| `waf` | Never sent: left out of a replace, cdn-api keeps the WAF attached, which `ezghcloud_cdn_distribution_waf` manages (a whole-document `PUT` of the distribution as read, plus `waf`, at its ETag; `null` on destroy) |
| `status` | `deploying` after every change until every location has it, then `deployed`; apply doesn't wait for it |
