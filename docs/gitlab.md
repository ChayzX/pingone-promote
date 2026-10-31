# GitLab production setup

Production writes must run through your private GitLab project and your own runners. You continue authoring and reviewing flows in the PingOne UI. This setup uses PingOne Configuration Management APIs; it does not require Terraform. Instance and tenant identities belong in private project settings and evidence, while credentials belong in protected variables. This public template contains no tenant configuration.

Production readiness remains blocked: [Ping's current documentation](https://docs.pingidentity.com/pingone/early-access-features/ea-p1_promote.html) limits Configuration Management early access to sandbox environments, and the disposable acceptance tenant returned a capability-disabled error for promotions. Confirm vendor production support and finish [live acceptance](HANDOFF.md) before using production credentials. GitLab-hosted runners used for disposable acceptance do not verify your production runner controls.

## 1. Create and protect the private project

Create a private project dedicated to promotion requests. The local application submits a branch and merge request there; the public tool repository contains no company requests or tenant data. Keep project visibility private and protect the default branch. Require your normal merge request approvals before merging requests. Add the supplied `examples/gitlab/.gitlab-ci.yml` and `examples/gitlab/select-request.sh` to this project, preserving their paths.

Pin `PROMOTION_TOOL_REF` to a reviewed, full 40-character commit SHA from the tool repository. This protected pin is used only by protected deployment and rollback jobs. Set `PROMOTION_VALIDATION_TOOL_REF` to a reviewed full 40-character SHA as a non-protected project variable so merge request validation can read it. Do not use a branch name or moving tag for either pin. Review and update the pins as part of your tool release process. If your runners cannot reach GitHub, mirror the source internally and update the clone URL in the template to that approved mirror.

## 2. Configure runners and protected settings

Register protected deployment runners with the tag `blue` or `green`. The template uses `blue` by default. Set the protected project variable `PROMOTION_RUNNER_TAG` to `green` to select a green runner. The value is a single tag: do not set it to both names unless one runner is registered with both tags. Register a separate unprotected runner with the `untrusted-validation` tag for merge request validation and set the non-protected `PROMOTION_VALIDATION_RUNNER_TAG` variable to its tag. Do not expose production variables or protected deployment runners to that validation runner. Docker runners use `PROMOTION_GO_IMAGE` (defaults to `golang:1.27`); shell runners ignore the image and need Go 1.27.3 or newer, Git, Bash 4.4+ and curl installed. Use your internal image mirror where required.

Set these project variables under **Settings → CI/CD → Variables**. Mark credentials masked and protected. Protect the production environment under **Operate → Environments** and require deployment approvals there. The GitLab tier must support protected environments and deployment approvals (Premium or Ultimate); if the APIs do not expose verifiable approval state, the job must fail closed. Configure approval rules on the production environment itself: merge request approval and deployment approval are separate controls.

| Variable | Setting |
| --- | --- |
| `PROMOTION_TOOL_REF` | Reviewed full commit SHA; mandatory placeholder in the template |
| `PROMOTION_VALIDATION_TOOL_REF` | Reviewed full commit SHA; non-protected project variable available to merge request jobs |
| `PROMOTION_RUNNER_TAG` | One runner tag: `blue` (default) or `green` |
| `PROMOTION_VALIDATION_RUNNER_TAG` | Tag for the separate untrusted validation runner; defaults to `untrusted-validation` |
| `PROMOTION_GITLAB_URL` | Trusted protected value equal to this GitLab instance's `CI_SERVER_URL` |
| `PROMOTION_GITLAB_PROJECT_ID` | Trusted protected numeric project ID equal to `CI_PROJECT_ID` |
| `PROMOTION_ENVIRONMENT` | Protected production environment name; defaults to `production` |
| `PINGONE_TARGET_ENVIRONMENT_ID` | Trusted target environment ID |
| `PINGONE_TARGET_REGION` | Trusted target region expected by PingOne |
| `PINGONE_CLIENT_ID` | PingOne worker client ID |
| `PINGONE_CLIENT_SECRET` | PingOne worker client secret; masked and protected |
| `PINGONE_AUTH_ENVIRONMENT_ID` | Optional auth environment for the worker credentials |
| `GITLAB_READ_API_TOKEN` | Masked, protected `read_api` token used by the verifier to inspect project protections and environment approvals |

Scope production credentials and trusted policy variables to the `production` environment (or the configured protected environment), in addition to masking/protecting credentials. Set the project's minimum role for overriding pipeline variables to Maintainer or stricter, and reserve that role for administrators of the deployment system. Requestors must not be able to override the trusted GitLab URL/project, target identity, environment name, either tool ref, or build image. “Protected variable” alone does not prevent higher-precedence pipeline-variable overrides. Verify these settings during acceptance; administrators who control pipeline code or independently hold production credentials remain outside the application's enforcement boundary.

`CI_JOB_TOKEN` is supplied by GitLab to each job and is used for runtime job identity and rollback artifact retrieval. Rollback additionally checks that the source artifact belongs to the same GitLab project and to a successful `plan_promotion` job on the protected default branch, with matching pipeline and commit identities. Rollback artifact download does not follow HTTP redirects while carrying a credential. If your GitLab setup redirects artifact downloads to object storage, configure a separately trusted fetch mechanism after reviewing its destination and credential handling; do not add blind redirect following. Give the PingOne worker only the roles needed for the configured operations. Production write credentials belong only in protected GitLab variables; never put them in request files, local profiles, scripts, artifacts or public CI variables. Keep untrusted merge request pipelines away from protected variables and production runners. The template uses a separate runner tag for merge request validation; never register that tag on a protected production runner.

The exact `PINGONE_TARGET_ENVIRONMENT_ID`, region, client setup and required role assignments depend on your tenant and must be established during live acceptance. Do not copy values from examples or guess them.

## 3. Submit and promote one request

Each local submission creates a separate `requests/<request-id>/request.json` in the private project and opens a merge request. Review the request and merge it to the protected default branch. Merge request pipelines validate request structure offline; they do not receive PingOne production credentials or make target changes. The post-merge pipeline identifies exactly one newly added request in the change. It stops if the change adds zero or multiple requests, so it will not redeploy older requests or select a “latest” file.

The protected default-branch push pipeline creates a native PingOne draft and publishes a short-lived, developer-visible plan artifact with the review digest and a Markdown change summary. The summary counts native steps by locally classified HTTP method and resource path; it is an estimate because PingOne does not return Terraform-style resource classifications. Review the complete plan and per-step before/after values in GitLab. The production deployment is a separate manual job protected by the production environment's approval rules. A manual web pipeline on the protected default branch can also create a plan when `PROMOTION_REQUEST_FILE` names one exact request path. The runner rechecks the GitLab job, branch, target identity, approvals and fresh plan state before execution. Retry is disabled because an uncertain native mutation requires status reconciliation, not automatic replay. Jobs for this target environment share a resource group; this serializes jobs but does not serialize whole pipelines, so the runner still checks for stale plans immediately before execution.

For a manually started web pipeline, `PROMOTION_REQUEST_FILE` may select one explicit `requests/<safe-id>/request.json` path. The helper rejects traversal and symlinks. The runner also requires the decoded request ID to match the selected `requests/<request-id>/request.json` directory; moving an old request under a new ID is rejected. Request and plan JSON reject duplicate keys and require exact schema field casing. Do not set it for merge request or push pipelines.

Artifacts contain redacted review data and metadata, not worker secrets or raw native write payloads. Keep project membership narrow; the configured developer artifact access permits project developers to read them. The apply artifact is retained for 90 days for audit. Do not include public or sensitive company data in requests. Secret redaction reduces exposure but does not guarantee removal of every possible secret-like value.

For a fresh diagnostic review of an existing plan artifact, run `pingone-runner inspect --artifact <existing-artifact.json> --out <new-diagnostic.json>` from a job accepted by the trusted GitLab verifier. It refreshes the existing native plan and writes a new artifact without creating or executing a plan. The output retains the original project, pipeline, commit and native identities. Running `inspect` does not approve, authorize or execute; its output remains subject to all normal apply gates. The command writes a diagnostic artifact with review issues before returning a blocked error, so retain that output for troubleshooting.

## 4. Create and approve a rollback

Rollback is a separate manual web pipeline on the protected default branch. Start it with `PROMOTION_MODE=rollback` and set `ORIGINAL_PLAN_JOB_ID` to the numeric job ID of the original `plan_promotion` job. The rollback planning job downloads that plan and creates a separate native rollback draft. Review its full scope and digest; then approve and run the separate protected `apply_rollback` deployment job. No rollback executes automatically. Git revert is not a native PingOne rollback. PingOne may require rollback of the latest promotion, and later manual changes can be overwritten; verify the live native constraints before use.

## 5. Network and operational controls

Keep TLS verification enabled. If your enterprise GitLab uses a private CA, install that CA in the runner image or host trust store; do not disable certificate checks. Make sure runners can reach the GitLab API, your approved source mirror and PingOne endpoints. A missing approval API, unsupported GitLab tier, mismatched trusted project or target, unprotected branch/environment, incomplete plan, or unavailable native API must block execution.

Before production, complete the live acceptance steps in [HANDOFF.md](HANDOFF.md). Template and fixture success alone do not establish compatibility with your GitLab version, project settings, runner tags, PingOne tenant or approval API responses.
