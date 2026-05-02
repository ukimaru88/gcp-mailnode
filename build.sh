#!/usr/bin/env bash
set -e

VERSION=$(cat version.txt | tr -d '[:space:]')
BUMP=${1:-patch}

IFS='.' read -r MAJOR MINOR PATCH <<< "$VERSION"
case "$BUMP" in
  major) MAJOR=$((MAJOR+1)); MINOR=0; PATCH=0 ;;
  minor) MINOR=$((MINOR+1)); PATCH=0 ;;
  *)     PATCH=$((PATCH+1)) ;;
esac
NEW_VERSION="${MAJOR}.${MINOR}.${PATCH}"

echo "Building gcp-mailnode v${NEW_VERSION}..."

mkdir -p releases

wails build \
  -ldflags "-X main.Version=${NEW_VERSION}" \
  -clean

echo "$NEW_VERSION" > version.txt

cp build/bin/gcp-mailnode.exe "releases/GCP-MailNode-v${NEW_VERSION}.exe"

echo "Done: releases/GCP-MailNode-v${NEW_VERSION}.exe"