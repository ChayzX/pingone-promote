#!/usr/bin/env bash
# fetch-tools.sh downloads the pinned tools pingone-promote runs, checks each
# file against the SHA-256 recorded below, and installs them into one
# directory. It never runs a tool it has not verified.
#
#   scripts/fetch-tools.sh [--dest DIR] [--with-go] [--with-pingcli]
#
# Default DIR is ./tools. Supported: macOS and Linux, amd64 and arm64.
#
# Installed:
#   terraform            1.16.7   the terraform fmt gate (the server never plans or applies)
#   pingcli-terraformer  0.6.0    the DaVinci exporter (ADR 0015)
# Optional:
#   --with-go            Go 1.27.3, to build pingone-promote from source (CI uses it)
#   --with-pingcli       pingcli 1.11.0. Not used by pingone-promote (ADR 0018:
#                        pingcli 1.x has no export command); offered only for
#                        operators who want it for diagnostics.
#
# Changing a version means changing its digests here in the same commit.
set -euo pipefail

TERRAFORM_VERSION=1.16.7
TERRAFORMER_VERSION=0.6.0
GO_VERSION=1.27.3
PINGCLI_VERSION=1.11.0

dest=./tools
with_go=0
with_pingcli=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dest) dest="$2"; shift 2 ;;
    --with-go) with_go=1; shift ;;
    --with-pingcli) with_pingcli=1; shift ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "unsupported operating system $(uname -s); use macOS or Linux" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac
plat="${os}_${arch}"

digest() {
  # digest TOOL PLATFORM -> expected sha256
  case "$1/$2" in
    terraform/darwin_amd64) echo d17d1053bcbde650a8a919b7218ef2886eac76c474306e9ecd8bd64427408741 ;;
    terraform/darwin_arm64) echo 81e98d9186e0cd1bc88caeb2403453fdd4bdeedd574ca4dbe8fb6a2880c9c51a ;;
    terraform/linux_amd64) echo 5454efcf7fc87f0c2113111b9a5c7182f0ca1c6f0f23a1e110a50770a8fe473b ;;
    terraform/linux_arm64) echo 2d6a3f1098de94f1672aaa4e6873ef91ade9eac38851d3a403e2009193208de4 ;;
    terraformer/darwin_amd64) echo 306c7064885012da5b9cc5579d59a3ba929d5ebd4c879f641254b36e2e0fab9f ;;
    terraformer/darwin_arm64) echo 80a18625c03b42745a1154491d71b597e738478c8997f9c72ac81f94fe63942b ;;
    terraformer/linux_amd64) echo be0bea0f4f7ecb4e33a95f9894793ae9c70bb3480665d9d303c06021b8ae4a98 ;;
    terraformer/linux_arm64) echo e934912a23b58418d673596c750281ba7dfe768ab0158e71eb4d63375aa156d7 ;;
    go/darwin_amd64) echo b6f8e931e4ab497fcde6da8ae4499a58f9d258e55c8a80824a516eae5950a750 ;;
    go/darwin_arm64) echo dd3908cf20959d798c1e06ebe5b72213f0505ce01373b40ce3110975a6cd99c2 ;;
    go/linux_amd64) echo 65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40 ;;
    go/linux_arm64) echo a06e99309dbcbdca1a8ccd2282c3ee146e55930886e26b4f1193269587165f49 ;;
    pingcli/darwin_amd64) echo f61039aeb25d48d8e60f530f22b21e42b91fb8c2bb2d51a8b817f4275ac7234e ;;
    pingcli/darwin_arm64) echo 5e63751d9fb62ec69cd961f2a8048dd95fe0782b1ade1148e8327a974f3e713b ;;
    pingcli/linux_amd64) echo a9e6ff01e59731391826c67c91ea89d6f62fd3fc5ffd6fe28e43f25b6020862b ;;
    pingcli/linux_arm64) echo 11b04b9c9ad1ba6b5c8b70592d5842949e6f91f9b7ff6617807d5f05f1064833 ;;
    *) echo "no pinned digest for $1 on $2" >&2; return 1 ;;
  esac
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$dest"
dest="$(cd "$dest" && pwd)"

fetch() {
  # fetch TOOL URL FILE
  local want got
  want="$(digest "$1" "$plat")"
  curl -fsSL --proto '=https' --tlsv1.2 -o "$work/$3" "$2"
  got="$(sha256_of "$work/$3")"
  if [ "$got" != "$want" ]; then
    echo "checksum mismatch for $3: expected $want, got $got; nothing was installed" >&2
    exit 1
  fi
  echo "verified $3"
}

fetch terraform "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_${plat}.zip" terraform.zip
(cd "$work" && unzip -o -q terraform.zip terraform)
install -m 0755 "$work/terraform" "$dest/terraform"

tf_name="pingcli-terraformer_${TERRAFORMER_VERSION}_${plat}"
fetch terraformer "https://github.com/pingidentity/pingcli-plugin-terraformer/releases/download/v${TERRAFORMER_VERSION}/${tf_name}" "$tf_name"
install -m 0755 "$work/$tf_name" "$dest/pingcli-terraformer"

if [ "$with_pingcli" = 1 ]; then
  pc_name="pingcli_${PINGCLI_VERSION}_${plat}"
  fetch pingcli "https://github.com/pingidentity/pingcli/releases/download/v${PINGCLI_VERSION}/${pc_name}" "$pc_name"
  install -m 0755 "$work/$pc_name" "$dest/pingcli"
fi

if [ "$with_go" = 1 ]; then
  go_name="go${GO_VERSION}.${os}-${arch}.tar.gz"
  fetch go "https://go.dev/dl/${go_name}" "$go_name"
  rm -rf "$dest/go"
  tar -C "$dest" -xzf "$work/$go_name"
fi

echo
echo "Installed into $dest:"
echo "  terraform            $TERRAFORM_VERSION"
echo "  pingcli-terraformer  $TERRAFORMER_VERSION  sha256 $(digest terraformer "$plat")"
[ "$with_pingcli" = 1 ] && echo "  pingcli              $PINGCLI_VERSION"
[ "$with_go" = 1 ] && echo "  go                   $GO_VERSION ($dest/go/bin)"
echo
echo "Put the pingcli-terraformer sha256 above in tools.terraformerSha256 of the server configuration."
