# PingOne Promote v0.2.0-rc.4

This preview adds clearer setup, structured logging controls, and more complete plan reviews for the macOS and Linux application. Homebrew installs the local browser app. Checksummed release bundles also include the CI helper used by private GitLab pipelines.

GitLab setup now explains the instance URL, numeric project ID, protected environment name, and project access token. A usable API token is required; an existing session token or process environment variable can supply it. Setup reports missing credentials when saving rather than deferring the problem to submission. Request submission also supports optional custom branch names and commit messages without overwriting an existing branch.

Environment profiles explain macOS Keychain and Linux Secret Service storage. New vault profiles require a secret, blank edits preserve an existing stored credential, and switching away removes the old vault copy. Handled metadata-write failures restore prior vault state; failed restoration is reported explicitly. Provider errors retain actionable guidance while excluding raw diagnostics. Linux requires a running, unlocked Secret Service provider. Native vault behavior still requires platform acceptance; browser fixtures use synthetic provider boundaries.

Logging settings in the UI control debug mode, minimum level, JSON/text format, and standard-error or private file output for the current app session. Failed destination changes preserve the active settings. Logs contain bounded operational fields and generated correlation IDs; request bodies, credentials, tenant configuration, raw errors, and authentication tokens are excluded.

Plan reviews include operation counts and escaped, redacted Markdown summaries for GitLab artifacts. Native flow review compatibility now preserves reviewed saved-version metadata while keeping publication bound to the selected published version. Existing target drift, digest, provenance, approval, and retry checks remain active.

Production planning, deployment, and rollback remain controlled by protected GitLab jobs. This is an installation preview, not a production-readiness declaration. A reviewed sandbox variable deployment completed through GitLab, but full live flow deployment, exclusion evidence, and successful native rollback acceptance remain outstanding. Fixture, package, and browser tests do not establish those live outcomes.

Stop the app before upgrading. Upgrades and uninstallations preserve profiles, history, and credentials stored outside the package. See `docs/installation.md` and `docs/gitlab.md` for setup.
