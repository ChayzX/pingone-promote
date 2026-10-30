#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
DIST="$ROOT/dist"
GO_BIN=${GO:-go}
VERSION=${VERSION:-dev}

case "$VERSION" in
  ''|*[!A-Za-z0-9.+-]*) printf 'Invalid build version: use a release version or dev\n' >&2; exit 1 ;;
esac
LDFLAGS="-X pingone-promote/internal/buildinfo.Version=$VERSION"

if ! command -v "$GO_BIN" >/dev/null 2>&1; then
	printf 'Go executable not found: %s\n' "$GO_BIN" >&2
	exit 1
fi

mkdir -p "$DIST"
cd "$ROOT"
: > "$DIST/SHA256SUMS"

checksum() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1"
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1"
	else
		printf 'A SHA-256 tool (sha256sum or shasum) is required\n' >&2
		return 1
	fi
}

for target in darwin/arm64 darwin/amd64 linux/amd64; do
	goos=${target%/*}
	goarch=${target#*/}
	name="pingone-promote-$goos-$goarch"
	binary="$DIST/$name"
	temp_bundle="$DIST/.bundle-$name"

	printf 'Building %s\n' "$name"
	GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 "$GO_BIN" build \
		-trimpath \
		-ldflags "$LDFLAGS" \
		-o "$binary" \
		./cmd/pingone-promote
	chmod 0755 "$binary"
	runner_name="pingone-runner-$goos-$goarch"
	GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags "$LDFLAGS" -o "$DIST/$runner_name" ./cmd/pingone-runner
	chmod 0755 "$DIST/$runner_name"

	rm -rf "$temp_bundle"
	mkdir -p "$temp_bundle"
	cp "$binary" "$temp_bundle/$name"
	cp "$ROOT/README.md" "$temp_bundle/README.md"
	cp "$DIST/$runner_name" "$temp_bundle/$runner_name"
	mkdir -p "$temp_bundle/docs" "$temp_bundle/examples/gitlab"
	cp "$ROOT/docs/gitlab.md" "$ROOT/docs/HANDOFF.md" "$ROOT/docs/verification.md" "$temp_bundle/docs/"
	cp "$ROOT/examples/gitlab/.gitlab-ci.yml" "$ROOT/examples/gitlab/select-request.sh" "$temp_bundle/examples/gitlab/"
	tar -C "$temp_bundle" -czf "$DIST/$name.tar.gz" "$name" "$runner_name" README.md docs examples
	(cd "$DIST" && checksum "$name.tar.gz") >> "$DIST/SHA256SUMS"
	rm -rf "$temp_bundle"
done

printf 'Release files are in %s\n' "$DIST"
