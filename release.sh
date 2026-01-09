#!/bin/bash
set -e

# Usage: ./release.sh v0.1.0

VERSION="$1"

if [ -z "$VERSION" ]; then
  echo "Usage: ./release.sh <version>"
  echo "Example: ./release.sh v0.1.0"
  exit 1
fi

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+ ]]; then
  echo "Error: Version must start with 'v' followed by semantic versioning (e.g., v1.0.0)"
  exit 1
fi

# Ensure working directory is clean
if [ -n "$(git status --porcelain)" ]; then
  echo "Error: Working directory is not clean. Please commit or stash changes first."
  exit 1
fi

echo "🔍 Running local tests..."
go test -tags sqlite_fts5 ./...
echo "✅ Tests passed."

echo "🚀 Preparing release $VERSION..."

# Create signed git tag (respects GPG/SSH signing keys and TUI prompts)
echo "🔏 Creating signed tag (this may prompt for your passphrase)..."
git tag -s "$VERSION" -m "Release $VERSION"
echo "✅ Signed tag created."

# Push tag to trigger CI/CD
# This respects GIT_SSH_COMMAND and other git environment variables.
echo "📡 Pushing tag to origin using your git environment..."
git push origin "$VERSION"

echo "🎉 Done! Watch the release build here:"
echo "   https://github.com/$(git config --get remote.origin.url | sed 's/.*github.com[:\/]//;s/\.git$//')/actions"
