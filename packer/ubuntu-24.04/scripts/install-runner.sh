#!/usr/bin/env bash
set -euo pipefail

: "${RUNNER_ARCH:?RUNNER_ARCH is required}"
: "${RUNNER_HOME:?RUNNER_HOME is required}"
: "${RUNNER_USER:?RUNNER_USER is required}"
: "${RUNNER_VERSION:?RUNNER_VERSION is required}"

export DEBIAN_FRONTEND=noninteractive

release_endpoint=https://api.github.com/repos/actions/runner/releases/latest
if [[ "${RUNNER_VERSION}" != latest ]]; then
  release_endpoint="https://api.github.com/repos/actions/runner/releases/tags/v${RUNNER_VERSION}"
fi

release_json=$(mktemp)
archive=$(mktemp --suffix=.tar.gz)
trap 'rm -f "${release_json}" "${archive}"' EXIT

curl \
  --fail \
  --location \
  --retry 5 \
  --retry-all-errors \
  --header 'Accept: application/vnd.github+json' \
  --header 'X-GitHub-Api-Version: 2022-11-28' \
  --output "${release_json}" \
  "${release_endpoint}"

mapfile -t release_details < <(python3 - "${release_json}" "${RUNNER_ARCH}" <<'PY'
import json
import re
import sys

with open(sys.argv[1], encoding="utf-8") as release_file:
    release = json.load(release_file)

version = release["tag_name"].removeprefix("v")
wanted = f"actions-runner-linux-{sys.argv[2]}-{version}.tar.gz"
for asset in release.get("assets", []):
    if asset.get("name") != wanted:
        continue
    digest = asset.get("digest", "").removeprefix("sha256:")
    if not re.fullmatch(r"[0-9a-fA-F]{64}", digest):
        raise SystemExit(f"release asset {wanted} has no valid SHA-256 digest")
    print(version)
    print(asset["browser_download_url"])
    print(digest.lower())
    break
else:
    raise SystemExit(f"release has no asset named {wanted}")
PY
)

resolved_version=${release_details[0]}
download_url=${release_details[1]}
sha256=${release_details[2]}

curl \
  --fail \
  --location \
  --retry 5 \
  --retry-all-errors \
  --output "${archive}" \
  "${download_url}"
echo "${sha256}  ${archive}" | sha256sum --check --strict

runner_group=$(id -gn "${RUNNER_USER}")
install -d -o "${RUNNER_USER}" -g "${runner_group}" -m 0755 "${RUNNER_HOME}"
tar --extract --gzip --file "${archive}" --directory "${RUNNER_HOME}"
"${RUNNER_HOME}/bin/installdependencies.sh"
chown -R "${RUNNER_USER}:${runner_group}" "${RUNNER_HOME}"

install -d -m 0755 /etc/actions-runner
printf 'version=%s\narchitecture=%s\nhome=%s\nuser=%s\n' \
  "${resolved_version}" "${RUNNER_ARCH}" "${RUNNER_HOME}" "${RUNNER_USER}" \
  > /etc/actions-runner/image-release
chmod 0644 /etc/actions-runner/image-release

test -x "${RUNNER_HOME}/run.sh"
