#!/usr/bin/env bash
set -euo pipefail

# Get the latest rigd release tag
latest=$(git tag --list 'rigd/v*' --sort=-v:refname | head -1)
if [ -z "$latest" ]; then
	echo "No existing rigd/v* tags found." >&2
	exit 1
fi

# Extract minor version and bump it
minor=$(echo "$latest" | sed 's|rigd/v0\.\([0-9]*\)\.0|\1|')
next=$((minor + 1))
tag="rigd/v0.${next}.0"

echo "Previous release: $latest"
echo "New release:      $tag"
echo ""

read -rp "Create and push tag? [y/N] " confirm
if [[ "$confirm" != [yY] ]]; then
	echo "Aborted."
	exit 0
fi

git tag -a "$tag" -m "Release $tag"
git push origin "$tag"

echo ""
echo "Tagged and pushed $tag"
