#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
DIST="$ROOT/dist"
GO_BIN=${GO:-go}

if ! command -v "$GO_BIN" >/dev/null 2>&1; then
	printf 'Go executable not found: %s\n' "$GO_BIN" >&2
	exit 1
fi

mkdir -p "$DIST"
cd "$ROOT"

for target in darwin/arm64 darwin/amd64 linux/amd64; do
	goos=${target%/*}
	goarch=${target#*/}
	name="pingone-promote-$goos-$goarch"
	binary="$DIST/$name"
	temp_bundle="$DIST/.bundle-$name"

	printf 'Building %s\n' "$name"
	GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 "$GO_BIN" build \
		-trimpath \
		-o "$binary" \
		./cmd/pingone-promote
	chmod 0755 "$binary"

	rm -rf "$temp_bundle"
	mkdir -p "$temp_bundle"
	cp "$binary" "$temp_bundle/$name"
	cp "$ROOT/README.md" "$temp_bundle/README.md"
	tar -C "$temp_bundle" -czf "$DIST/$name.tar.gz" "$name" README.md
	rm -rf "$temp_bundle"
done

printf 'Release files are in %s\n' "$DIST"
