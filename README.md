# PingOne Promote

> Integration handoff: the GitLab-controlled workflow is on `feat/gitlab-production`. Start with [docs/HANDOFF.md](docs/HANDOFF.md). Local execution is denied by default; live GitLab/PingOne acceptance and a complete production-readiness review remain outstanding. The older `v0.1.0`/`main` build does not contain this new policy.

PingOne Promote is a local browser application for reviewing and promoting explicitly selected PingOne configuration resources with PingOne's native Configuration Management API. It requires Go 1.27.3 or newer to build and has no third-party runtime dependencies. The API must be enabled for the tenant.

## Requirements

- Go 1.27.3+ to build, or a prebuilt release binary.
- A PingOne worker OAuth client with [Promotion Admin](https://docs.pingidentity.com/pingone/directory/p1_roles.html) explicitly assigned in **both** the source and destination environments, plus the read permissions needed for environment and resource discovery. The environments must belong to the same organization. Environment Admin alone does not establish promotion access.
- The worker client's authentication environment may differ from the managed environment. Set each profile's Authentication environment ID accordingly.

Configuration Management must be opted in and permitted by the environment's license. An opt-in checkbox alone does not prove entitlement. Inventory uses native snapshot creation; a `403` at resource loading can therefore reflect missing snapshot permissions or an unavailable capability. The tool includes safe access-denial detail codes, such as `LICENSE_EXCEEDED`, when PingOne returns them.

The app supports the configuration resource types exposed by PingOne's promotion snapshots. It does not promote users, runtime data, or administrator role assignments. A DaVinci flow promotion uses the latest **deployed** source version under PingOne's native rules, not the latest saved draft.

## Build and run

Homebrew preview installation and package lifecycle instructions are in [docs/installation.md](docs/installation.md). Homebrew installs the local application; GitLab builds the runner separately. Both tools support `--version`; reporting the version does not start the app or access credentials.

Build the current platform binary:

```sh
go build -o pingone-promote ./cmd/pingone-promote
```

Start the local UI:

```sh
./pingone-promote --listen 127.0.0.1:8787
```

Destinations use the GitLab submission workflow by default. Configure a **private** GitLab project through the UI and install the reviewed [pipeline template](examples/gitlab/.gitlab-ci.yml) using [the setup guide](docs/gitlab.md). Company configuration is committed only to that private project.

For a disposable or explicitly designated non-production environment only, startup option `--local-target NON_PRODUCTION_ENVIRONMENT_ID` enables the original local deploy/rollback workflow for that exact ID. Repeat the flag for additional non-production IDs. Renaming a profile does not change the execution policy. Never allowlist production. Keeping production write credentials only in protected GitLab variables and enforcing PingOne roles is the actual organization-wide credential boundary; a local application cannot prevent administrators from using credentials they independently hold.

The app prints a one-time bootstrap URL for the current run. Open that URL in your browser; the bootstrap credential is generated again at every start. Keep the server bound to loopback. `--no-browser` disables automatic browser opening, and `--data-dir PATH` selects where local profiles and operation history are stored. Use `--help` to see the available options.

### Structured logs

The UI server writes newline-delimited JSON events to stderr by default. Set `--log-level debug|info|warn|error` and `--log-format json|text` to adjust output. On macOS and Linux, use `--log-file PATH` to append logs to a file; the app creates parent directories with owner-only permissions and secures the file as `0600`. File write failures fall back to stderr, and a file that cannot be opened or secured prevents startup. Operators are responsible for log rotation and retention. File logging is disabled on Windows because the standard library cannot establish a private ACL; use stderr or configure a private ACL outside the application before redirecting stderr.

The same settings can be supplied as `PINGONE_PROMOTE_LOG_LEVEL`, `PINGONE_PROMOTE_LOG_FORMAT`, and `PINGONE_PROMOTE_LOG_FILE`; CLI flags override these values. `pingone-runner` uses these environment settings and writes its existing result JSON/stdout unchanged; its failure event goes to stderr or the configured log file.

Authenticated users can open **Logging settings** in the UI to change the minimum level, include bounded debug dispatch events, choose JSON or text, and switch between stderr and a private file. UI changes last only until the app exits and do not modify environment variables or command-line defaults. A failed destination change leaves the active sink and settings unchanged. On Windows, the file option is visibly disabled with the reason. Debug events add only the fixed route, method, and request ID; they do not include request data.

Events contain severity, a fixed event and stage, an allowlisted route name, standard HTTP method, status, duration, and a generated request correlation ID. Request IDs are returned in the `X-Request-ID` response header; application failure events carry the same ID as the request completion event. Route names never include path segments; URLs, query strings, hosts, headers, cookies, request bodies, native PingOne payloads, tenant/environment identifiers, profile configuration, credentials, bootstrap/CSRF tokens, and personal data are excluded. Error strings and stack traces are never serialized. Treat logs as operational records, but still protect and retain them according to local policy.

Session-only client secrets must be entered again after each app restart. To use a process environment variable, set it before starting the app, then save a profile with the `Environment variable` credential option and that variable's name:

```sh
export PINGONE_CLIENT_SECRET='your-worker-client-secret'
./pingone-promote --listen 127.0.0.1:8787
```

The variable value is read when the app needs the credential. Profiles may instead store the credential in the operating system keyring: macOS Keychain, Windows Credential Manager, or Linux Secret Service (such as GNOME Keyring or KWallet). Linux requires a running and unlocked Secret Service provider. If the system credential vault is unavailable or locked, the app reports the error and does not save the secret in a local file. Existing macOS Keychain entries continue to use the same service and profile account. Profile and activity JSON do not contain client secrets or API tokens.

## Promotion workflow and safety

1. Save profiles for the source and destination environments, then load resources.
2. Search and select only the resources to promote. Dependencies remain unselected until explicitly selected. Notification templates are never automatically approved.
3. Review every operation in the generated native plan before deploying. The app checks that the full plan remains within the approved selection and destination scope. If PingOne reports promotion-variable errors, resolve them in the PingOne console, rebuild the plan, and review it again.
4. Deploy only after the plan is ready and its complete review is valid. If an execute request has an ambiguous timeout, do not retry it manually; refresh history and reconcile the operation with PingOne first.

An empty destination mapping lets PingOne apply its native automatic mapping or create behavior when no match exists. PingOne retains resource mappings across promotions, so an earlier native mapping may still select an existing destination resource; an empty mapping does not guarantee a new resource will be created.

Rollback creates a separate native rollback plan that must be reviewed and executed. PingOne only permits rollback of the latest promotion into that destination, and rollback covers the complete promotion. It may overwrite manual destination changes made since the promotion.

## Acceptance check for notification-template isolation

Run this against a non-production tenant before relying on the tool for a real promotion. The native API's exclusion and dependency-reuse behavior still needs live confirmation.

1. Change an unrelated notification template in the source environment, leaving it unselected.
2. Select only the intended flow, build the plan, and inspect **every page** of its operations. Confirm no notification-template operation appears.
3. Record the destination template state, deploy the selected flow, then verify the destination template is unchanged.
4. Roll back that promotion, review and execute the separate rollback plan, then verify the destination template is still unchanged.
5. In a separate promotion, explicitly select the changed template and confirm that only the chosen template changes.

Also verify any dependency intended for reuse remains unchanged. A blocked plan or unresolved variable error should be corrected in PingOne and regenerated, not bypassed.

## RHEL over SSH

Run the binary on the RHEL host with a loopback listener and an environment variable credential source. Do not expose the app's port publicly. From the workstation, open a tunnel:

```sh
ssh -L 8787:127.0.0.1:8787 user@rhel-host
```

Then open the one-time bootstrap URL using the forwarded local port. The host can use `--no-browser` because it may not have a desktop session.

## Build release bundles

Run `scripts/build.sh` to cross-build CGO-free binaries for macOS arm64/amd64 and Linux amd64. The script honors `GO` (default `go`) and `VERSION` (default `dev`), and writes binaries, `.tar.gz` bundles and `SHA256SUMS` under `dist/`. Each bundle includes both commands, documentation and the GitLab template. See [the release process](docs/installation.md#maintainer-release-process) for versioned source exports, Homebrew formula generation and draft prereleases.

## Validation status

The app has not been accepted against a live PingOne tenant, and macOS Keychain behavior has not been runtime-tested in this environment. Perform the acceptance check above, including snapshot completeness, exclusions, dependency reuse, flow mapping, and rollback, before treating those wire behaviors as verified.

## PingOne API references

- [Configuration Management API overview](https://developer.pingidentity.com/pingone-api-ea/platform/early-access/configuration-management/index.html)
- [Create a snapshot](https://developer.pingidentity.com/pingone-api-ea/platform/early-access/configuration-management/snapshots/create-snapshot.html) and [read embedded snapshots](https://developer.pingidentity.com/pingone-api-ea/platform/early-access/configuration-management/snapshots/get-embedded-snapshots.html)
- [Create a promotion](https://developer.pingidentity.com/pingone-api-ea/platform/early-access/configuration-management/promotions/create-promotion.html), [inspect promotion steps](https://developer.pingidentity.com/pingone-api-ea/platform/early-access/configuration-management/promotions/get-promotion-steps.html), and [excluded resources](https://developer.pingidentity.com/pingone-api-ea/platform/early-access/configuration-management/promotions/excluded-resources.html)
- [Rollback a promotion](https://developer.pingidentity.com/pingone-api-ea/platform/early-access/configuration-management/promotions/rollback-promotion.html)

## Repeat browser verification

Development-only browser checks require Python Playwright and an existing Chromium executable. Start the fixture from the repository root (it never connects to PingOne):

```sh
PINGONE_BROWSER_FIXTURE_FILE=/tmp/pingone-browser.json go test ./internal/app -run TestBrowserFixture -v -timeout=20m
```

In another terminal, run:

```sh
python3 scripts/test_browser.py --fixture-file /tmp/pingone-browser.json --chromium /path/to/chromium
python3 scripts/test_browser_races.py --fixture-file /tmp/pingone-browser.json --chromium /path/to/chromium
python3 scripts/test_browser_gitlab.py --fixture-file /tmp/pingone-browser.json --chromium /path/to/chromium
python3 scripts/test_browser_logging.py --fixture-file /tmp/pingone-browser.json --chromium /path/to/chromium --output-dir /tmp/pingone-logging-evidence
```

Stop the fixture with Ctrl+C when finished. Screenshots go to `/tmp/pingone-browser-evidence` unless `--output-dir` is supplied. These checks verify local behavior only. See [verification notes](docs/verification.md) for the live acceptance checklist and API limitations.
