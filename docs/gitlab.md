# GitLab production setup

Production writes run through your private GitLab project and your own runners. You continue authoring and reviewing flows in the PingOne UI. This setup uses PingOne Configuration Management APIs; it does not require Terraform. The GitLab URL, project ID, target environment ID, PingOne test IDs and credentials were not available in this workspace, so the template has not been connected to a live tenant.

## 1. Create and protect the private project

Create a private project dedicated to promotion requests. The local application submits a branch and merge request there; the public tool repository contains no company requests or tenant data. Keep project visibility private and protect the default branch. Require your normal merge request approvals before merging requests. Add the supplied `examples/gitlab/.gitlab-ci.yml` and `examples/gitlab/select-request.sh` to this project, preserving their paths.

Pin `PROMOTION_TOOL_REF` to a reviewed, full 40-character commit SHA from the tool repository. Do not use a branch name or a moving tag. Review and update the pin as part of your tool release process. If your runners cannot reach GitHub, mirror the source internally and update the clone URL in the template to that approved mirror.

## 2. Configure runners and protected settings

Register your own GitLab runners with the tag `blue` or `green`. The template uses `blue` by default. Set the protected project variable `PROMOTION_RUNNER_TAG` to `green` to select a green runner. The value is a single tag: do not set it to both names unless one runner is registered with both tags. Docker runners use `PROMOTION_GO_IMAGE` (defaults to `golang:1.27`); shell runners ignore the image and need Go 1.24 or newer, Git, Bash 4.4+ and curl installed. Use your internal image mirror where required.

Set these project variables under **Settings → CI/CD → Variables**. Mark credentials masked and protected. Protect the production environment under **Operate → Environments** and require deployment approvals there. The GitLab tier must support protected environments and deployment approvals (Premium or Ultimate); if the APIs do not expose verifiable approval state, the job must fail closed. Configure approval rules on the production environment itself: merge request approval and deployment approval are separate controls.

| Variable | Setting |
| --- | --- |
| `PROMOTION_TOOL_REF` | Reviewed full commit SHA; mandatory placeholder in the template |
| `PROMOTION_RUNNER_TAG` | One runner tag: `blue` (default) or `green` |
| `PROMOTION_GITLAB_URL` | Trusted protected value equal to this GitLab instance's `CI_SERVER_URL` |
| `PROMOTION_GITLAB_PROJECT_ID` | Trusted protected numeric project ID equal to `CI_PROJECT_ID` |
| `PROMOTION_ENVIRONMENT` | Protected production environment name; defaults to `production` |
| `PINGONE_TARGET_ENVIRONMENT_ID` | Trusted target environment ID |
| `PINGONE_TARGET_REGION` | Trusted target region expected by PingOne |
| `PINGONE_CLIENT_ID` | PingOne worker client ID |
| `PINGONE_CLIENT_SECRET` | PingOne worker client secret; masked and protected |
| `PINGONE_AUTH_ENVIRONMENT_ID` | Optional auth environment for the worker credentials |
| `GITLAB_READ_API_TOKEN` | Masked, protected `read_api` token used by the verifier to inspect project protections and environment approvals |

Scope production credentials and trusted policy variables to the `production` environment (or the configured protected environment), in addition to masking/protecting credentials. Set the project's minimum role for overriding pipeline variables to Maintainer or stricter, and reserve that role for administrators of the deployment system. Requestors must not be able to override the trusted GitLab URL/project, target identity, environment name, tool ref, or build image. “Protected variable” alone does not prevent higher-precedence pipeline-variable overrides. Verify these settings during acceptance; administrators who control pipeline code or independently hold production credentials remain outside the application's enforcement boundary.

`CI_JOB_TOKEN` is supplied by GitLab to each job and is used for runtime job identity and rollback artifact retrieval. Rollback artifact download does not follow HTTP redirects while carrying a credential. If your GitLab setup redirects artifact downloads to object storage, configure a separately trusted fetch mechanism after reviewing its destination and credential handling; do not add blind redirect following. Give the PingOne worker only the roles needed for the configured operations. Production write credentials belong only in protected GitLab variables; never put them in request files, local profiles, scripts, artifacts or public CI variables. Keep untrusted merge request pipelines away from protected variables and production runners. Use a separate uncredentialed runner for merge request validation if your protected runner cannot safely accept untrusted pipeline code.

The exact `PINGONE_TARGET_ENVIRONMENT_ID`, region, client setup and required role assignments depend on your tenant and must be established during live acceptance. Do not copy values from examples or guess them.

## 3. Submit and promote one request

Each local submission creates a separate `requests/<request-id>/request.json` in the private project and opens a merge request. Review the request and merge it to the protected default branch. Merge request pipelines validate request structure offline; they do not receive PingOne production credentials or make target changes. The post-merge pipeline identifies exactly one newly added request in the change. It stops if the change adds zero or multiple requests, so it will not redeploy older requests or select a “latest” file.

The protected default-branch push pipeline creates a native PingOne draft and publishes a short-lived, developer-visible plan artifact with the review digest. Review the complete plan in GitLab. The production deployment is a separate manual job protected by the production environment's approval rules. A manual web pipeline on the protected default branch can also create a plan when `PROMOTION_REQUEST_FILE` names one exact request path. The runner rechecks the GitLab job, branch, target identity, approvals and fresh plan state before execution. Retry is disabled because an uncertain native mutation requires status reconciliation, not automatic replay. Jobs for this target environment share a resource group; this serializes jobs but does not serialize whole pipelines, so the runner still checks for stale plans immediately before execution.

For a manually started web pipeline, `PROMOTION_REQUEST_FILE` may select one explicit `requests/<safe-id>/request.json` path. The helper rejects traversal and symlinks. Do not set it for merge request or push pipelines.

Artifacts contain redacted review data and metadata, not worker secrets or raw native write payloads. Keep project membership narrow; the configured developer artifact access permits project developers to read them. The apply artifact is retained for 90 days for audit. Do not include public or sensitive company data in requests. Secret redaction reduces exposure but does not guarantee removal of every possible secret-like value.

### Requests from DaVinci

The [DaVinci Request flow](../examples/davinci-request-flow/README.md) replaces the local app for requesters. Admins sign in with PingOne SSO, and the flow commits the same `requests/<id>/request.json` the local app makes and opens the merge request. Everything above applies unchanged. The flow also reports, in the merge request, whether each secret in the selection has a sensitive promotion variable.

### Intents from the DaVinci front door

A DaVinci front-door flow ([examples/davinci-front-door](../examples/davinci-front-door/README.md)) can submit an intent instead of a full request: a merge request that adds exactly one `intents/<intent-id>/intent.json`. An intent names the signed-in PingOne requester, the source and target environments, the selected resource IDs, any mappings and a description. It carries no configuration.

- **Merge request pipelines** run `pingone-runner validate --intent`. That check is strict: it rejects unknown, duplicate or case-variant keys, and an ID that doesn't match its `intents/<id>/` folder.
- **After merge**, the protected `plan_promotion` job runs `pingone-runner build-request`. It first proves it is the live protected job for the protected target, then takes fresh snapshots and writes `built-request.json` with the same redaction and fingerprints as a locally built request, reusing the intent's ID. Planning, approval and apply then proceed exactly as above.
- **Review:** reviewers approve the intent in the merge request, and the full redacted configuration in `plan-review.md` at deployment approval.
- **Selection:** a change that adds an intent and a request together, or two intents, is refused. Manual web pipelines accept only explicit request paths, never intents.

## 4. Create and approve a rollback

Rollback is a separate manual web pipeline on the protected default branch. Start it with `PROMOTION_MODE=rollback` and set `ORIGINAL_PLAN_JOB_ID` to the numeric job ID of the original `plan_promotion` job. The rollback planning job downloads that plan and creates a separate native rollback draft. Review its full scope and digest; then approve and run the separate protected `apply_rollback` deployment job. No rollback executes automatically. Git revert is not a native PingOne rollback. PingOne may require rollback of the latest promotion, and later manual changes can be overwritten; verify the live native constraints before use.

## 5. Network and operational controls

Keep TLS verification enabled. If your enterprise GitLab uses a private CA, install that CA in the runner image or host trust store; do not disable certificate checks. Make sure runners can reach the GitLab API, your approved source mirror and PingOne endpoints. A missing approval API, unsupported GitLab tier, mismatched trusted project or target, unprotected branch/environment, incomplete plan, or unavailable native API must block execution.

Before production, complete the live acceptance steps in [HANDOFF.md](HANDOFF.md). Template and fixture success alone do not establish compatibility with your GitLab version, project settings, runner tags, PingOne tenant or approval API responses.

## 6. Automated acceptance checks

Three `pingone-runner` commands automate the repeatable parts of the live acceptance checklist. None of them changes configuration; `evidence capture` and `preflight` only read, apart from the native snapshot that capture creates (planning creates one too). They never start pipelines or approve deployments: those remain human steps, and API- or GraphQL-started pipelines are rejected by the workflow rules.

| Command | Where it runs | What it checks |
| --- | --- | --- |
| `preflight` | Manual pipeline with `PROMOTION_MODE=preflight` (job `preflight`) | Required CI/CD variables present, plus near-miss spellings (for example `PINGONE_CLIENT_SECRE`); private active project; protected default branch; minimum role for pipeline variables; pipeline-triggerer self-approval; protected environment with at least one approval; job-token identity; for each PingOne environment, identity, Configuration Management opt-in and whether the promotions API answers. It reports "feature not active (license/entitlement)" separately from "worker lacks a role", because PingOne returns HTTP 403 for both. Set `PREFLIGHT_SOURCE_ENVIRONMENT_ID` to probe the source too. |
| `plan-check` | Automatically in `plan_promotion` and `rollback_plan` | Digest file matches the review digest; no review issues; every step stays inside the target environment; every promotion step comes from a selected source resource; optional forbidden resource types (`PROMOTION_FORBID_TYPES`, comma-separated URL path segments such as `templates`); rollback linkage; no known secret value (worker secret, read token, job token, evidence key) appears in the artifact. Writes `plan-review.md` / `rollback-review.md` for approvers. Locally you can add `--expect-selected id1,id2`. |
| `evidence capture` / `evidence compare` | Capture: manual pipeline with `PROMOTION_MODE=evidence`, `EVIDENCE_ENVIRONMENT_ID`, `EVIDENCE_LABEL`. Compare: anywhere, on two downloaded captures | Capture stores an HMAC-SHA256 fingerprint of every resource, keyed by `EVIDENCE_HMAC_KEY` or the worker secret, and never stores configuration values. Compare fails on any change, removal or addition that was not declared with `--expect-changed` or `--allow-added-type`, and on captures made with different keys or environments. |

Example before/after check for one promotion, run on your workstation after downloading both evidence artifacts:

```sh
./pingone-runner evidence compare --before before-hp4.json --after after-hp4.json \
  --expect-changed <target-flow-id> --report hp4-evidence.md
```

Keep evidence files and reports in the private project or your private evidence log; they contain environment and resource identifiers.
