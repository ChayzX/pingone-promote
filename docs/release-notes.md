# PingOne Promote installation preview

This preview adds Homebrew packaging for the local browser application and the GitLab runner CLI. Both commands report their installed version with `--version`. Release sources and platform archives have SHA-256 checksums.

The operations console includes profile-dialog fixes, contextual errors, stale-response protection and safeguards against replaying pending or uncertain writes. Production planning, deployment and rollback remain controlled by protected GitLab jobs.

This is an installation preview, not a production-readiness declaration. Live native PingOne promotion and rollback acceptance remain outstanding because the disposable trial's capability is unavailable despite opt-in. Fixture, release and browser tests do not establish live API behavior. Review the installation matrix for platform runtime evidence; macOS Keychain has separate acceptance requirements.

Upgrades and uninstallations preserve profiles, history and credentials stored outside the package. Stop the app before upgrading. See `docs/installation.md` and `docs/gitlab.md` for installation and workflow setup.
