# Releasing

## Publish a version

Run the Release workflow from main with the next unused semantic version:

```sh
gh workflow run release.yml --ref main -f version=0.1.0
```

Alternatively create and push a tag on a main commit:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The release waits for the reusable CI workflow: formatting, vet, race tests,
Terraform lifecycle tests, generated documentation drift and cross-platform builds.
It builds six archives, a protocol manifest, checksums and a detached binary GPG
signature. The script checks every checksum and the signature before uploading a
draft release, then publishes it after all assets are present. Stable versions become
latest; prereleases stay prereleases. Existing releases are never overwritten.

Manual workflow tags are created by `GITHUB_TOKEN`; GitHub suppresses another tag
workflow for that push, so the same manual run performs the publication.

## Signing

The dedicated RSA release signing public key is
[release-signing.asc](../keys/release-signing.asc), fingerprint:

```text
082E86B6B4C657BF928F0259C5ACE64236843174
```

CI uses secrets `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE`, and variable `GPG_KEY_ID`.
Key recovery material is maintained outside this repository. Do not replace the
signing key without adding its public half to the Registry first.

To verify downloaded assets, import the public key and run:

```sh
gpg --import keys/release-signing.asc
gpg --verify terraform-provider-ezghcloud_0.1.0_SHA256SUMS.sig terraform-provider-ezghcloud_0.1.0_SHA256SUMS
shasum -a 256 -c terraform-provider-ezghcloud_0.1.0_SHA256SUMS
```

## Terraform Registry onboarding

GitHub releases work independently of Registry registration. To make the provider
installable as `ezgamehost/ezghcloud` through the public Registry:

1. Sign in to the Terraform Registry with an account authorized for `ezgamehost`.
2. Add the public key above to the organization's signing keys.
3. Choose Publish > Provider and select `ezgamehost/terraform-provider-ezghcloud`.
4. Confirm the released version was ingested. The Registry creates a release webhook
   for subsequent versions; use Resync in provider settings if ingestion stalls.

Until that registration is complete, use the local development instructions in the
README. Publishing a GitHub release alone does not register the provider.

Reference: [HashiCorp provider publishing requirements](https://developer.hashicorp.com/terraform/registry/providers/publishing).
