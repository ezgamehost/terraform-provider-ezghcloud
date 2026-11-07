# Terraform Provider for EZGH Cloud

Manage EZGH Cloud infrastructure with Terraform.

This provider is under development. Build it locally using the instructions below;
there is no published Terraform Registry release yet.

The provider covers projects, VPCs, subnets, firewalls, SSH keys,
instances, volumes, databases with their backups, and file systems with their mount targets
and access points, DNS zones and record sets, Web Certificates domain authorizations
and certificates, Kubernetes clusters with their node groups, add-ons, access entries and pod
identity associations, WAFs and their load balancer attachments, plus IAM bots, groups, policies, memberships, policy
attachments, API keys, invitations, and federation configuration, with import and
lookup data sources where supported.

See the [provider design](docs/superpowers/specs/2026-11-01-terraform-provider-design.md).

## Development

Requires Go 1.27.1 or newer. The provider uses HashiCorp's Terraform Plugin Framework
and plugin protocol 6. Tests exercise Terraform 1.17.1.

```sh
go build -o bin/terraform-provider-ezghcloud .
go test -race ./...
go vet ./...
```

Configure a Terraform CLI development override in a local CLI configuration file,
replacing the path with the absolute path to this checkout's `bin` directory:

```hcl
provider_installation {
  dev_overrides {
    "ezgamehost/ezghcloud" = "/absolute/path/to/terraform-provider-ezghcloud/bin"
  }
  direct {}
}
```

Set `TF_CLI_CONFIG_FILE` to that file. With a development override, run `terraform
validate`, `plan`, and `apply` directly; `terraform init` still tries to install a
published provider. See [provider configuration](docs/index.md),
[infrastructure examples](examples/compute), [network examples](examples/network),
[database examples](examples/databases) and [IAM examples](examples/iam).

```sh
export EZGHCLOUD_API_TOKEN='your-bearer-credential'
export EZGHCLOUD_ORGANIZATION_ID='your-organization-id'
```

Use API or bot keys for ordinary infrastructure management. Creating API keys
requires a user OAuth bearer credential and the required IAM permissions. The
provider accepts a user access token but does not log in or refresh OAuth tokens.

## State and resource ownership

API key tokens and instance user data are sensitive attributes, but Terraform state
still stores them. Protect your state backend and access to saved plans. SSH key
resources upload public keys; they do not generate private keys. Imported API keys
cannot recover their original token.

Firewall rules belong to the firewall resource as one complete collection. IAM
membership and policy attachment resources each manage a single relationship.
Instance launch topology changes require replacement. Independently managed volumes
remain independent when attached to an instance. See the generated
[resource references](docs/resources) for each schema and its import syntax.

API responses have both UUID identifiers and prefixed identifiers. IAM and project
resources expose the API's ID plus a separate `slug`; compute, storage and network
resources expose their prefixed identifier. Use resource attribute references rather
than constructing or converting identifiers yourself.

## Verification

```sh
# Includes real Terraform plan/apply/import/update/destroy against local API fixtures.
TF_ACC=1 go test -race -timeout 7m ./...

# Regenerate schema reference documentation; requires Terraform on PATH.
go tool tfplugindocs generate --provider-name ezghcloud
```

Default tests and acceptance fixtures create no real cloud resources. The only live test, `TestLiveDatabaseLifecycle`,
passed against production on 2026-11-03 (create, resize 20 to 30 GiB, destroy; 244 s). Do not point fixture tests at a live endpoint.

## Releases

Push a `vMAJOR.MINOR.PATCH` tag or manually run the Release workflow with a version.
The workflow runs the full CI suite, builds Linux, macOS and Windows binaries for
amd64 and arm64, verifies signed checksums, and publishes a GitHub release. Manual
runs create the version tag from main after verification. Published versions are
immutable; prereleases do not replace the latest stable version.

Repository signing secrets are configured. The public key is in
[keys/release-signing.asc](keys/release-signing.asc). Terraform Registry onboarding
requires adding that key and registering the provider once; subsequent GitHub
releases are picked up by the Registry webhook. See [releasing](docs/releasing.md)
for release commands, verification, and onboarding. Private signing material is
kept outside the repository.

Licensed under [MPL-2.0](LICENSE).
