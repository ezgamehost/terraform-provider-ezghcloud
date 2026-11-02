# IAM implementation

The IAM package uses org-api's public routes. API `id` values are used for references
and imports. IAM identities currently return UUID IDs and separate prefixed slugs;
the slug is not substituted for a UUID-only request field.

| Resource | Mutable fields | Replacement fields | Import |
| --- | --- | --- | --- |
| bot | name | organization | org/id |
| group | name, description | organization | org/id |
| policy | name, description, statements | organization | org/id |
| oidc_provider | name, audiences, uploaded JWKS rotation | issuer, organization, discovery/uploaded key source transitions | org/id |
| api_key | none | name, bot_id, expires_in_seconds | org/id |
| invitation | none | email, group_ids, policy_ids, expires_in_days | org/id |
| bot_trust | trust | bot_id | org/bot_id |
| bot_oidc_trust | none | bot_id, provider_id, subject, claims | org/bot_id/entry_id |
| group_membership | none | group_id, principal_type, principal_id | org/type/principal_id/group_id |
| policy_attachment | none | principal_type, principal_id, policy_id | org/type/principal_id/policy_id |

URL-escape each import component, particularly SSO resource names containing `/`.
Import the policy by its API ID, not its display name. Managed policies are lookup-only.
Data sources for users, bots, groups, policies and OIDC providers require their API ID.

Bot/group/policy/OIDC updates and relationship writes fetch the current ETag and send
If-Match. A conflict is returned to Terraform. Membership and attachment resources use
only the single-relationship endpoint and preserve relationships owned elsewhere.
Bot trust owns the complete SSO trust list: destroying it clears that list.

API key creation requires a user's OAuth bearer token. API keys cannot create API
keys; the server enforces this restriction. The provider can use an externally
obtained user access token in `api_token`; it does not perform OAuth login or refresh.
Key refresh requires `apiKeys.list` so absence is not confused with an owner-filtered
response. Tokens are sensitive computed state values, returned once and preserved
on refresh. Imports cannot recover the token. Expiry/name/owner changes replace a key;
there is no implicit rotation.

When a relationship's parent is missing, refresh confirms the organization remains
visible before removing the relationship from state. Destroy treats that confirmed
absence as success. If the organization is hidden, refresh and destroy return an
error. Update and delete validate the persisted organization before any HTTP request,
including when Terraform refresh is disabled.

Invitations create a pending invitation and shareable login URL. The platform does
not send email. Accepted, declined, expired and revoked invitations remain visible
with their server status, without automatically inviting the person again. Destroy
revokes only a still-pending invitation; it never removes an organization member.
An expired invitation is already ineffective and needs no revocation.

Optional omitted attributes retain null in state when the API supplies defaults.
One-time tokens and configured public JWKS JSON are preserved rather than reconstructed
from server output. Users and SSO/SCIM directory identities cannot be created through
this package; invite people and manage their access relationships instead.

API fixtures verify CRUD wire shapes, conditional relationship isolation, state
encoding, nested policy conversion, import escaping, and one-time token preservation.
No live IAM mutations were run during development.
