#!/usr/bin/env bash
set -euo pipefail
version="${1:?Pass the version without v}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$ ]] || { echo 'Invalid release version' >&2; exit 1; }
: "${GPG_PRIVATE_KEY:?Configure the release signing secret}"
: "${GPG_KEY_ID:?Configure the release signing key ID}"
release_dir="$(mktemp -d)"
trap 'rm -rf "$release_dir"' EXIT
export GNUPGHOME="$release_dir/gnupg"
mkdir -m 700 "$GNUPGHOME"
printf '%s' "$GPG_PRIVATE_KEY" | gpg --batch --import
unset GPG_PRIVATE_KEY
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}" arch="${target#*/}"
  binary="terraform-provider-ezghcloud_v${version}"
  [[ "$os" != windows ]] || binary+='.exe'
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$release_dir/$binary" .
  (cd "$release_dir" && zip -q "terraform-provider-ezghcloud_${version}_${os}_${arch}.zip" "$binary")
  rm "$release_dir/$binary"
done
cp terraform-registry-manifest.json "$release_dir/terraform-provider-ezghcloud_${version}_manifest.json"
(cd "$release_dir" && sha256sum ./*.zip ./*_manifest.json | sed 's|  ./|  |' > "terraform-provider-ezghcloud_${version}_SHA256SUMS")
printf '%s' "${GPG_PASSPHRASE:-}" | gpg --batch --yes --pinentry-mode loopback --passphrase-fd 0 --local-user "$GPG_KEY_ID" --detach-sign "$release_dir/terraform-provider-ezghcloud_${version}_SHA256SUMS"
gh release create "v$version" "$release_dir"/*.zip "$release_dir"/*_manifest.json "$release_dir"/*_SHA256SUMS "$release_dir"/*_SHA256SUMS.sig --verify-tag --generate-notes
