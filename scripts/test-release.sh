#!/usr/bin/env bash
set -euo pipefail
for version in '01.0.0' '1.00.0' '1.0.01' '1.0' '1.0.0;echo injected' '1.0.0-' '1.0.0-01' '1.0.0-alpha.01'; do
  if output="$(env -u GPG_PRIVATE_KEY -u GPG_KEY_ID bash scripts/release.sh "$version" 2>&1)"; then
    echo "Accepted invalid version: $version" >&2
    exit 1
  fi
  if [[ "$output" != *'Invalid release version'* ]]; then
    echo "Version was not rejected before signing setup: $version" >&2
    exit 1
  fi
done
for version in '0.1.0' '1.2.3' '1.2.3-rc.1' '1.2.3-alpha--beta'; do
  bash scripts/release.sh "$version" --validate-version
done
echo 'Release version validation passed'
