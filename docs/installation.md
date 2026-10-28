# Installation

Homebrew packaging is a preview. It does not establish live PingOne promotion or rollback compatibility. Production planning, deployment and rollback remain GitLab-only.

## Homebrew

The project tap uses versioned, SHA-256-verified release sources. Homebrew installs Go as a build dependency and compiles both commands; users do not need to configure Go manually. The installed commands have no separate language runtime requirement.

After the preview release and project tap are published:

```sh
brew install chayzx/tap/pingone-promote
pingone-promote --version
pingone-runner --version
pingone-promote
```

The interface opens in the default browser and listens on localhost. `--no-browser` prints its startup link instead. This package does not register or start a background service.

Supported installation tests cover macOS Apple Silicon, macOS Intel and Linux x86_64 with a supported Homebrew installation. Successful builds alone do not establish runtime acceptance. Consult the release's installation checks before installing a preview. RHEL runtime and macOS Keychain acceptance are separate checks.

## Upgrade and uninstall

```sh
brew update
brew upgrade chayzx/tap/pingone-promote
brew uninstall --force pingone-promote
```

Stop the running application before upgrading. It prints its profile/history directory at startup. The default is `~/Library/Application Support/pingone-promote` on macOS, or `$XDG_CONFIG_HOME/pingone-promote` (normally `~/.config/pingone-promote`) on Linux. `--data-dir` selects another directory.

The uninstall command removes all installed versions of this package, including older versions Homebrew may have retained. Homebrew upgrades and uninstallations preserve the external data directory and macOS Keychain items. Session-only credentials disappear when the app exits. Package archives contain source, executables and documentation, never local profiles, history or credentials.

## Downloaded archives

Each preview also includes versioned macOS arm64/amd64 and Linux amd64 bundles. Download the matching archive and `SHA256SUMS` from the same release and verify the checksum before extracting it:

```sh
awk '$2 == "pingone-promote-v0.2.0-rc.1-darwin-arm64.tar.gz"' SHA256SUMS | shasum -a 256 -c -
```

Replace the filename with the exact version and platform you downloaded. On Linux, use `sha256sum -c -` after the same `awk` selection. Verify that the selected archive is explicitly reported `OK`. Bundles include both commands, the GitLab setup documentation and pipeline template. These archives do not install or manage services.

## Maintainer release process

The read-only Homebrew workflow installs a generated formula through a temporary local tap on each test host. It tests both commands, authenticated local startup, request protections, an actual version upgrade and uninstall, and verifies external profile/history bytes remain unchanged. It refuses existing installations and dirty or mismatched source trees.

Build from a committed source revision with Python 3.12+, Git, Go and a POSIX shell:

```sh
python3 scripts/release.py release --version v0.2.0-rc.1 --ref HEAD --output dist/release-v0.2.0-rc.1
python3 scripts/release.py verify --directory dist/release-v0.2.0-rc.1
```

The source archive is exported from the requested Git object, including when the module is a subdirectory. Untracked or uncommitted working files cannot enter it. Platform binaries are built from that exported source with the release version stamped into both commands. The formula pins the source archive URL and checksum; it never downloads a moving branch at install time.

The manually triggered release workflow reruns Homebrew installation checks and produces a **draft prerelease**. It does not deploy PingOne resources or update the tap. Before publication, review the exact source revision, matrix results, package checksum verification, release notes and privacy scan. Do not replace published assets under an existing version: fix forward with a new version.

After publishing that reviewed prerelease, copy its generated `pingone-promote.rb` to `Formula/pingone-promote.rb` in `ChayzX/homebrew-tap`. Publish only the formula referencing the public HTTPS release asset. The `--local-source` formula mode is for isolated package tests and must never be published. Verify a clean install from the published tap before announcing availability.
