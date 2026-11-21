#!/usr/bin/env bash
# Upgrade this Redlaunch installation to the latest (or a given) GitHub release
# without compiling anything on the server.
#
# Usage: scripts/upgrade.sh [vMAJOR.MINOR.PATCH]
#
# Environment:
#   REDLAUNCH_INSTALL_DIR  installation directory (default: parent of scripts/)
#   REDLAUNCH_REPOSITORY   GitHub owner/name (default: redbolttechnologies/redlaunch)

set -Eeuo pipefail

fail() {
	printf 'upgrade: %s\n' "$*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

readonly repository=${REDLAUNCH_REPOSITORY:-redbolttechnologies/redlaunch}
readonly version_pattern='^v[0-9]+\.[0-9]+\.[0-9]+$'
install_dir=${REDLAUNCH_INSTALL_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
version=${1:-}

[[ $# -le 1 ]] || fail 'usage: scripts/upgrade.sh [vMAJOR.MINOR.PATCH]'
[[ -z $version || $version =~ $version_pattern ]] || fail "version must look like v1.2.3: $version"

for command_name in curl docker git sha256sum gunzip; do
	require_command "$command_name"
done
docker info >/dev/null 2>&1 || fail 'cannot talk to Docker; run as root or as a user in the docker group'
docker compose version >/dev/null 2>&1 || fail 'the Docker Compose plugin is required'

case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "unsupported architecture: $(uname -m)" ;;
esac

cd "$install_dir" || fail "installation directory not found: $install_dir"
[[ -f docker-compose.yml && -d .git ]] || fail "$install_dir is not a Redlaunch installation"
[[ -z $(git status --porcelain --untracked-files=no) ]] ||
	fail 'the installation has local changes to tracked files; commit or discard them first'

if [[ -z $version ]]; then
	version=$(curl -fsSL "https://api.github.com/repos/$repository/releases/latest" |
		sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1) ||
		fail 'could not query the latest release'
	[[ $version =~ $version_pattern ]] || fail "unexpected latest release tag: ${version:-none}"
fi

# Use the image name the Compose file will start (REDLAUNCH_IMAGE in .env).
image=redlaunch:local
if [[ -f .env ]]; then
	configured=$(sed -n 's/^REDLAUNCH_IMAGE=//p' .env | tail -n 1 | tr -d "\"'")
	[[ -z $configured ]] || image=$configured
fi

current=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$image" 2>/dev/null || true)
printf 'Installed: %s\nTarget:    %s (%s)\n' "${current:-unknown}" "$version" "$arch"
if [[ $current == "$version" ]]; then
	printf 'Already up to date.\n'
	exit 0
fi

work_dir=$(mktemp -d) || fail 'could not create a temporary directory'
trap 'rm -rf "$work_dir"' EXIT

base_url="https://github.com/$repository/releases/download/$version"
archive="redlaunch-image-linux-$arch.tar.gz"
printf 'Downloading %s...\n' "$archive"
curl -fsSL -o "$work_dir/$archive" "$base_url/$archive" || fail "could not download $archive for $version"
curl -fsSL -o "$work_dir/SHA256SUMS" "$base_url/SHA256SUMS" || fail "could not download SHA256SUMS for $version"

expected=$(awk -v name="$archive" '$2 == name {print $1}' "$work_dir/SHA256SUMS")
[[ -n $expected ]] || fail "SHA256SUMS has no entry for $archive"
actual=$(sha256sum "$work_dir/$archive" | awk '{print $1}')
[[ $expected == "$actual" ]] || fail "checksum mismatch for $archive"
printf 'Checksum verified.\n'

# --force: a release tag that was re-created upstream must replace the stale local one.
git fetch --tags --force || fail 'could not fetch tags from the Git remote (see the error above)'
git rev-parse --verify --quiet "refs/tags/$version" >/dev/null || fail "tag $version not found in the repository"

gunzip -c "$work_dir/$archive" | docker load >/dev/null || fail 'could not load the release image'
docker tag "redlaunch:$version" "$image" || fail "could not tag redlaunch:$version as $image"
git checkout --quiet --detach "$version" || fail "could not check out $version"

printf 'Recreating the Redlaunch app container...\n'
docker compose up -d --no-build --pull never --no-deps --force-recreate app

printf 'Upgraded to %s. Check it with: docker compose logs --tail 50 app\n' "$version"
