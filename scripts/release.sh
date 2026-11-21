#!/usr/bin/env bash
# Create and push the next vMAJOR.MINOR.PATCH release tag.
# Usage: scripts/release.sh patch|minor|major
set -euo pipefail

bump="${1:-}"
case "${bump}" in
  patch|minor|major) ;;
  *) echo "usage: $0 patch|minor|major" >&2; exit 2 ;;
esac

remote="${REMOTE:-origin}"

if [[ -n "$(git status --porcelain)" ]]; then
  echo "working tree is not clean; commit or stash changes first" >&2
  exit 1
fi

git fetch --tags --quiet "${remote}"

# Highest strict vMAJOR.MINOR.PATCH tag; other tags are ignored.
latest="$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true)"

if [[ -z "${latest}" ]]; then
  major=0 minor=0 patch=0
else
  IFS=. read -r major minor patch <<< "${latest#v}"
fi

case "${bump}" in
  patch) patch=$((patch + 1)) ;;
  minor) minor=$((minor + 1)); patch=0 ;;
  major) major=$((major + 1)); minor=0; patch=0 ;;
esac

next="v${major}.${minor}.${patch}"
echo "latest tag: ${latest:-none}"
echo "new tag:    ${next} ($(git rev-parse --short HEAD))"

git tag -a "${next}" -m "Release ${next}"
git push "${remote}" "${next}"
