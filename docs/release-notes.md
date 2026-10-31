# PingOne Promote v0.2.0-rc.5

This preview fixes native promotion error handling and updates the workflow documentation. Homebrew installs the macOS or Linux local browser app. GitLab builds its runner separately from a reviewed source pin; checksummed release bundles also include that helper.

PingOne can return native failures in `promotionErrors` rather than legacy `errors`. Both fields now block error-bearing plans and supply bounded, sanitized summaries to the app and runner. Failed execution retains its intent marker and remains available for status reconciliation and separately reviewed rollback planning. Arbitrary native messages, codes, and identifiers are excluded from diagnostics. This improves reporting; it does not repair an upstream PingOne service failure or automatically retry execution.

GitLab setup now explains the instance URL, numeric project ID, protected environment name, and project access token. A usable API token is required; an existing session token or process environment variable can supply it. Setup reports missing credentials when saving rather than deferring the problem to submission. Request submission also supports optional custom branch names and commit messages without overwriting an existing branch.

Environment profiles explain macOS Keychain and Linux Secret Service storage. New vault profiles require a secret, blank edits preserve an existing stored credential, and switching away removes the old vault copy. Handled metadata-write failures restore prior vault state; failed restoration is reported explicitly. Provider errors retain actionable guidance while excluding raw diagnostics. Linux requires a running, unlocked Secret Service provider. Native vault behavior still requires platform acceptance; browser fixtures use synthetic provider boundaries.

Logging settings in the UI control debug mode, minimum level, JSON/text format, and standard-error or private file output for the current app session. Failed destination changes preserve the active settings. Logs contain bounded operational fields and generated correlation IDs; request bodies, credentials, tenant configuration, raw errors, and authentication tokens are excluded.

Plan reviews include operation counts and escaped, redacted Markdown summaries for GitLab artifacts. Native flow review compatibility now preserves reviewed saved-version metadata while keeping publication bound to the selected published version. Existing target drift, digest, provenance, approval, and retry checks remain active.

Production planning, deployment, and rollback remain controlled by protected GitLab jobs. This is an installation preview, not a production-readiness declaration. A reviewed sandbox variable deployment completed through GitLab. A later sandbox flow operation created a destination flow and version before PingOne returned HTTP 500; complete resource effects and recovery remain unverified. Full live flow deployment, exclusion evidence, and successful native rollback acceptance remain outstanding. Fixture, package, and browser tests do not establish those live outcomes.

Stop the app before upgrading. Upgrades and uninstallations preserve profiles, history, and credentials stored outside the package. See `docs/installation.md` and `docs/gitlab.md` for setup.
