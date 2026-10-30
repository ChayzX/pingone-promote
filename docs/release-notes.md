# PingOne Promote v0.2.0-rc.2

This preview fixes profile credential-mode saves and strengthens reviewed GitLab promotions. Homebrew installs the local browser application; the checksummed macOS/Linux bundles also include the runner CLI for GitLab setup.

Profile saves now omit inactive credential fields. Switching from an environment variable to session storage no longer submits the hidden variable name, and environment-variable mode omits any previously typed direct secret. Browser regressions cover saves, profile edits and the Keychain request payload; actual Keychain storage requires macOS acceptance.

Promotion safeguards now detect concurrent collection creates even when native steps carry a future resource ID. Drift blocks before an execute-intent marker is written. GitLab authorization checks the authenticated job's pipeline source, and pipeline shell guards stop explicitly when trusted settings are missing or mismatched.

Request and plan files reject duplicate keys and incorrectly cased schema fields. Request IDs must match their selected folders. Redaction covers secret schema markers, case-insensitive sensitivity flags, passphrases, connection strings, authorization, DSNs and connection URIs. Builds require Go 1.27.3 or newer. Existing rollback provenance, approval and retry safeguards remain in place.

Production planning, deployment and rollback remain controlled by protected GitLab jobs. Tenant data and credentials are not included in these packages.

This is an installation preview, not a production-readiness declaration. Live native PingOne promotion and rollback acceptance remain outstanding because the disposable trial's capability is unavailable despite opt-in. Fixture, release and browser tests do not establish live API behavior. Review the installation matrix for platform runtime evidence; macOS Keychain has separate acceptance requirements.

Upgrades and uninstallations preserve profiles, history and credentials stored outside the package. Stop the app before upgrading. See `docs/installation.md` and `docs/gitlab.md` for installation and workflow setup.
