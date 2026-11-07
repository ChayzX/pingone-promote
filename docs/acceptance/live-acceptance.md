# Live acceptance: DaVinci app + GitLab request job

This plan is for you to run in your own environments. Nobody else has access to them.

Each test lists:
- **Do**: what to do
- **Expect**: what should happen
- **Capture**: the evidence to send back

Fill in the [results form](#results-form-send-this-back) at the end and send it back. Evidence must be **sanitized**: no tokens, client secrets, environment IDs, project IDs, user emails or flow contents. Use names, status words, counts and timings instead.

**Stop rule.** If any **[STOP]** test fails, stop there and send the results. Later tests depend on it.

> Tests marked **(engine PR)** depend on the request job from the v2 engine PR (#96). Run groups 0–2 now, and groups 3–6 after that PR merges.
>
> Groups 3–6 use the names from that PR:
> - the CI file: `examples/gitlab-ci/pingone-promote.yml`, installed as `.gitlab/pingone-promote.yml`;
> - the jobs: `pingone-promote:request` and `pingone-promote:check`;
> - the variables, artifact and log line: `PINGONE_PROMOTE_*`, `result.json` and `PINGONE_PROMOTE_RESULT`.
>
> `docs/gitlab-ci-integration.md` is the reference for all of them.

---

## 0. What you need

| Item | Notes |
| --- | --- |
| A **non-production** PingOne environment to export from (call it `dev`) | Must contain at least 2 DaVinci flows. One of them should call a subflow and use a connection and a variable. |
| At least one **downstream** non-production environment (call it `qa`) | Must be one your pipeline can apply to. Use a second one (`stage`) if you have it. |
| A **read-only worker** app in PingOne | Role: DaVinci Admin Read Only on `dev`. Client ID and secret. |
| A PingOne **SSO application and group** for the DaVinci app | See `davinci-app/README.md` §1. Add yourself to the group. |
| A **disposable copy** of your config repo in GitLab | It must not be the production config repo. It needs the same enterprise `include:` and protected `main`, and its environments must point at the non-prod envs above. |
| Runner access with your tags | The same runners your pipeline uses today. |
| Two GitLab project access tokens on the disposable repo | Both Developer role with `api` scope. Token **A** is for the CI request job (CI variable `PINGONE_PROMOTE_GITLAB_TOKEN`). Token **B** is for DaVinci (company variable `ppGitlabToken`). |

Timing: groups 0–2 take about 1 hour. Groups 3–6 take about 2–3 hours, including waiting on pipelines.

---

## 1. Setup checks

| ID | Do | Expect | Capture |
| --- | --- | --- | --- |
| **S-1 [STOP]** | Follow `davinci-app/README.md` §1–2. Create the SSO app, policy and group. Import `davinci-app/flow/pingone-promote-app.flow.json`. | The import succeeds with no errors. The flow shows about 76 nodes. | Pass/fail. Paste any import error text. |
| **S-2 [STOP]** | Set the company variables `ppWorkerBasicAuth` and `ppGitlabToken` (token **B**). Set the company variables from README "Configure after import" (they survive a re-import; an empty one uses its default). Then import the flow a second time and check that the values are still set (DESIGN.md §12, assumption 16). Use `ppRolloutEnvs` = your real order, e.g. `qa,stage,prod`, and set `ppApplyJobPattern` / `ppEnvironmentPattern` to match your pipeline's job and environment names. | Saved. | The values of `ppApplyJobPattern`, `ppEnvironmentPattern` and `ppRolloutEnvs` (these names are not secret). |
| **S-3** | Open the app before setting one variable, e.g. temporarily blank `ppGitlabProjectId`. | You see "PingOne Promote is not set up yet", the setting **name** is listed, and no value is shown. Restore the variable afterwards. | Screenshot with values blurred. |
| **S-4 (engine PR)** | In the disposable repo, follow these steps. (1) Copy `examples/gitlab-ci/pingone-promote.yml` to `.gitlab/pingone-promote.yml`. (2) Add `- local: '/.gitlab/pingone-promote.yml'` after the enterprise `include:`. (3) In that file, set `PINGONE_PROMOTE_SOURCES` (your source, e.g. `dev`) and `PINGONE_PROMOTE_ROLLOUT` (e.g. `qa,stage,prod`) to the same names as `ppSourceEnvs` and `ppRolloutEnvs`, and set `PINGONE_PROMOTE_REPO_URL`, `PINGONE_PROMOTE_REF` and `PINGONE_PROMOTE_RUNNER_TAG`. (4) Add the masked, protected project variables `PINGONE_PROMOTE_WORKER_CLIENT_ID`, `PINGONE_PROMOTE_WORKER_CLIENT_SECRET`, `PINGONE_PROMOTE_WORKER_ENVIRONMENT_ID`, `PINGONE_PROMOTE_GITLAB_TOKEN` (token **A**), plus `PINGONE_PROMOTE_REGION_CODE`. Leave the optional `PINGONE_PROMOTE_TERRAFORMER_SHA256` unset. (5) Protect the wildcard `pingone-promote/*` with Developers + Maintainers allowed to push and merge. | The MR pipeline that adds the file passes `pingone-promote:check`. Enterprise jobs behave as before. | Pipeline status. Job names that ran. Whether any job failed on the terraformer pin (with the digest unset it should not; a version error means `TERRAFORMER_VERSION` and the build disagree). |
| **S-5 (engine PR)** | Run the existing pipeline once on `main` without any request. | Neither `pingone-promote:request` nor `pingone-promote:check` runs. Your normal jobs run, and the env jobs stay manual. | List of jobs that ran. |
| **S-6** | Check your GitLab tier and whether `qa`/`stage`/`prod` are **protected environments** with approvals or manual jobs. | Either is fine; just record which. | Tier. Gate type per env. |

---

## 2. DaVinci app: sign-in, search, screens

These tests check the 12 DaVinci assumptions in `davinci-app/DESIGN.md` §12. The assumption number is shown in brackets after each test ID.

| ID | Do | Expect | Capture |
| --- | --- | --- | --- |
| **D-01 [STOP] (A1)** | Open the app as a user **in** the allowed group. | You are signed in through PingOne SSO and land on "New request". Your name or username shows in the header. | Pass/fail. Does your display name appear correctly? |
| **D-02** | Open the app as a user **not** in the group. Or temporarily set `ppAllowedGroups` to a group you're not in. | Access is refused, with a clear message. No app screens appear. | Screenshot. |
| **D-03 [STOP] (A2, A3)** | On "New request", pick source `dev` and type part of a flow name. | Matching flows are listed. The buttons work, which means the page script bound correctly. | Time from click to result (seconds). Number of flows in `dev`. |
| **D-04 (A11)** | Search with a term that matches nothing, then one that matches several flows. | "No flows match" in the first case. In the second, a list to choose from: the app never auto-picks. | Pass/fail each. |
| **D-05** | Search by an exact flow **ID** (copy it from the DaVinci console). | Exactly that flow is selected. | Pass/fail. |
| **D-06 (A4)** | If you can, temporarily rename a dev flow to include `{{test}}` or `<b>x</b>`, then search for it. | The name shows literally. It is not rendered as HTML and not resolved as a variable. Rename it back afterwards. | Screenshot. |
| **D-07** | Search again in the same source without reloading. | The results are instant, because the app reuses the earlier list and makes no new API call. | Time (seconds). |
| **D-08 (A11)** | If `dev` has more than about 50 flows, search for one near the end of the alphabet. | Found, which shows paging works. | Pass/fail. Total flows. |
| **D-09 (A12)** | — | Covered by S-1: if the import succeeded, the function size is fine. | — |
| **D-10 (A10)** | Leave the app idle for 35 minutes on a screen, then click something. | Either it continues, or you see a clean "session ended" page and can start again. It must not show a broken page. | What happened, and after how many minutes. |

---

## 3. Preview (dry-run request) (engine PR)

| ID | Do | Expect | Capture |
| --- | --- | --- | --- |
| **E-01 [STOP] (A7)** | Pick a flow that has a subflow, a connection and a variable. Click **Preview**. | A branch `pingone-promote/<requestId>` appears, created from `main` by one commit (`pingone-promote: preview request <requestId>`) that adds `.pingone-promote/request.json`. A push pipeline starts, and `pingone-promote:request` runs in `.pre` (resource group `pingone-promote-requests`). | Seconds until the pipeline appeared. Seconds the job ran. |
| **E-02 [STOP] (A6)** | Wait on the Preview screen. | The job's artifact `result.json` has `resultVersion: 1` and `status: previewed`, or `blocked` if values are missing. The screen shows the changes, each with an action (added, updated, kept differs, unchanged, moved) and a status (new, changed in source, matches, Git is newer, changed in both). The selected flow is marked. Readiness lists blockers per env (`missing_value` rows are inline inputs, `_request` shows as "Whole request"). Required secrets show the Vault path and whether `customer_secrets.yml` declares them. | Screenshot of the summary with names only. Total seconds from click to preview. |
| **E-03** | Open the `pingone-promote:request` job log in GitLab. | The log shows the pinned build (`PINGONE_PROMOTE_REF`, `TERRAFORMER_VERSION`/`TERRAFORMER_COMMIT`, the `TERRAFORM_SHA256` check), the terraformer export of **only** the selected flow and its dependencies, and `terraform fmt`. The last line is `PINGONE_PROMOTE_RESULT {...}`. No secret values appear. | Pass/fail. Paste any line that looks like a secret, redacted. |
| **E-04** | Check the branch after the preview. | Only the request commit is there, and `.pingone-promote/request.json` is still present: a preview never commits. | Pass/fail. |
| **E-05** | Check the enterprise jobs in the request branch pipeline. | They behave as on any branch: plan at most, and **no apply runs**, since the env jobs are manual. `pingone-promote:check` does not run (it runs only in MR pipelines). | Jobs that ran. Confirm nothing applied. |
| **E-05b** | Fill in a readiness value, then preview again. | A second commit on the same branch updates `request.json` (the Commits API `update` with `last_commit_id`), and a new job runs. Entering a secret-looking value, or a title longer than 255 characters, is refused on the screen before anything is committed. | Pass/fail each. |

---

## 4. Submit, the generated MR, and the additive merge (engine PR)

| ID | Do | Expect | Capture |
| --- | --- | --- | --- |
| **E-06 [STOP]** | Click **Submit** (MR mode). | `result.json` has `status: submitted`, `commitId`, `mrUrl`, `mrIid` and `diffUrl` (the commit page). The branch has ONE generated commit with the `Requested-by:`, `Request-id:` and `PingOne-Promote-Request:` trailers, and that commit deletes `.pingone-promote/request.json`. An MR to `main` opens, labelled `pingone-promote` (`PINGONE_PROMOTE_MR_LABELS`), whose description ends with the `Requested-by:` and `Request-id:` lines. The next pipeline on the branch does not run `pingone-promote:request`. The app shows the MR link. | Seconds to MR. MR files-changed count. |
| **E-07 [STOP]** | Review the MR diff (or `files[]` in `result.json`). | Only allow-listed paths change: `definitions/*.tf`, the root `.tf` files, `<env>.tfvars` (`PINGONE_PROMOTE_OVERLAY_PATTERN`, default `{env}.tfvars`) and `customer_secrets.yml`, plus the deletion of the request file. Only blocks for the selected flow, its **new** dependencies, and the variables and overlay values they need have changed. Every other flow and connection is **unchanged**. Nothing is deleted. | Files changed. Confirm "no unrelated changes" and "no deletions". |
| **E-08** | In the MR, check the `<env>.tfvars` files, `secrets.tf` and `customer_secrets.yml`. | Each env file gets only new required values. `secrets.tf` gets new secret variables, if any. `customer_secrets.yml` gets new `TF_VAR_` entries at `apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/<field>@secret` only for secrets that were `declared: false`. Existing entries and comments are untouched. | Pass/fail per file. |
| **E-09** | The MR pipeline runs `pingone-promote:check` and the enterprise plan jobs. | `pingone-promote:check` passes (fmt, overlays complete for `PINGONE_PROMOTE_SOURCES` and `PINGONE_PROMOTE_ROLLOUT`, secrets declared). The plans for rollout envs show only the expected adds and changes. | Plan summary per env, as add/change/destroy counts. |
| **E-10** | Before merging, change a **shared** connection used by the flow in dev, then do a new request for the same flow. | The connection shows as **kept differs** (the Git version is kept), with the reason from `changes[].reason`. You can switch it to Update (`dependencyChoices: update`) or to Exclude (`exclude`, see E-17), and the default stays Keep. An address changed in both Git and the source shows "changed in both" and offers the same choice. | Pass/fail. |
| **E-11** | Repeat E-06 with **Branch only** mode on another flow. | `status: submitted`, `branchUrl` set, no `mrUrl`. One generated commit, and no MR. The app shows the branch link and "Nothing rolls out until it is merged". | Pass/fail. |
| **E-12** | Submit the same request twice: click Submit again, or use Check GitLab after a refresh. | The second run reports `status: already_submitted`, with the existing `commitId` and `mrUrl`. There is no second commit and no second MR. The app shows "already submitted" and the existing link. | Pass/fail. |
| **E-13** | Run a request for the **same flow again** with no changes in dev. | The preview shows every row as matches Git, and Submit is off ("Nothing to change"). If submitted anyway, `status: no_changes`: the job only deletes the request file and opens no MR, and the app shows "No changes". | Pass/fail. |
| **E-14** | The app always sends the flow ID, so this one is done by hand. Push a branch `pingone-promote/test-2` whose `.pingone-promote/request.json` selects `flows: [{"name": "<a word two dev flows share>"}]`, with no ID. | `status: failed`, `error.code: preflight`, `error.type: validation`, with `candidates`. The app lists the candidates by name. | Pass/fail. |
| **E-15** | In the app choose **Whole environment** for a small sandbox source env (a few flows, one application, two connections, one variable), tick "Bring the whole <source> environment into Git", **Continue**, and **Preview**. Then Submit. Run the same request again after merging the MR. | The flow search is gone and Continue is refused without the tick. `request.json` has `scope: "all"`, `confirmAll: true`, `flows: []`. The job exports with **no** `--include-resources`; `result.json` has `scope: "all"`, `summary.byType` and the complete `changes` list (role `environment`). The preview leads with counts by type and folds the list. Everything new is added; anything that differs from Git is **Differs; Git version kept** unless you choose Update; **nothing is deleted** and no existing block changes. The MR title starts with `[whole environment]`. The second run reports every row as matching Git (no changes). | `request.json`, the `summary.byType` counts, seconds for the preview, MR files-changed count. Pass/fail for "nothing deleted" and "the re-run is a no-op". |
| **E-16** | In a whole-environment or a selected request open **Exclude items** and exclude one flow that Git already manages (search by name; try a name that matches two flows first). Preview, Submit. | A name matching two flows lists both and the app picks none; the job answers `preflight` with `candidates` if it still cannot tell. `request.json` has `exclude: [{type: "flow", id, name}]`; the job log shows `--exclude-resources` for the flow and its deploy and enable. The excluded flow is listed under **Excluded** with its reason, is **not** in the exported blocks, and its blocks in Git are byte-identical in the MR. Excluding the same flow you included is refused on the screen, naming both. | The `--exclude-resources` arguments, MR diff for the flow's file (unchanged), screenshot of the Excluded list. |
| **E-17** | Request a flow that uses a connection (or subflow) Git does **not** manage. After the first preview choose **Exclude** for that dependency and preview again. Fill the ID the resource has in qa, stage and prod; leave the source value as filled. Submit. Then repeat with a dependency Git **does** manage (choose Exclude for it). | For the unmanaged dependency the preview shows ``excluded; referenced by ID variable `<type>_<label>_id` ``, and qa, stage and prod each show a `missing_value` blocker that names the dependency until you give its ID (the source env's is prefilled from the export). The MR declares the variable in `variables.tf` and `definitions/variables.tf`, passes it in the module call, and sets it in every `<env>.tfvars`; the flow refers to `var.<type>_<label>_id`, and the excluded resource is not written. `pingone-promote:check` and the plans pass. For the managed one the reason is `excluded; Git-managed copy kept`, references keep pointing at the Git address, and no variable is added (the same files as Keep Git version). | The variable name, the `<env>.tfvars` lines (IDs may be shown), plan summaries. Pass/fail for "no variable for a managed dependency". |

---

## 5. Rollout and status (engine PR)

| ID | Do | Expect | Capture |
| --- | --- | --- | --- |
| **P-01 [STOP]** | Merge the MR from E-06. | The post-merge pipeline starts on `main`. GitLab deletes the request branch on merge. In the app, the request shows the rollout strip: qa Waiting/Running, the rest Waiting. | Pass/fail. |
| **P-02 (A8)** | Run or approve the qa env job (manual job or protected-environment approval). | qa goes **Needs approval** (with "n of m approvals" if approvals are used), then **Running**, then **Live**. The flow appears in qa. | Seconds per state change in the app versus GitLab. Does "n of m" show? |
| **P-03 (A9)** | Leave stage or prod unapproved. | The strip shows them as **Needs approval** or **Waiting**. | Pass/fail. |
| **P-04** | Check that dev was **not** applied (dev is in `PINGONE_PROMOTE_SOURCES` only, never in `PINGONE_PROMOTE_ROLLOUT`). | No apply job ran for dev. | Pass/fail. |
| **P-05** | Make a rollout apply fail on purpose in a safe way, e.g. remove the Vault value for a new secret in `stage`, if you have one. | The app shows stage as **Failed**, with the job link, and later envs stay Waiting. The plan error lists the missing secret **name** only. | Screenshot. |
| **P-06** | The Requests page. | Your MRs labelled `pingone-promote` are listed with *Requested by* (from the MR description's `Requested-by:` line) and their state. Request branches without an MR are listed separately. | Screenshot with names only. |

---

## 6. Failure handling and security (engine PR)

| ID | Do | Expect | Capture |
| --- | --- | --- | --- |
| **X-01** | Start a Preview, then cancel the GitLab pipeline before `pingone-promote:request` runs. | After `ppPipelineAppearSeconds` (120 s) or `ppRunTimeoutSeconds` (600 s), the app shows "no pipeline yet" or "GitLab is slow", with **Check GitLab**. Check GitLab recovers or explains the state. Submit is never stuck disabled. | Pass/fail. |
| **X-02** | Two people (or two browser sessions) request the **same flow** at the same time, and both submit. | The two jobs run one at a time (`resource_group: pingone-promote-requests`; the app shows "waiting for another request to finish"). If the second one's base has moved, it gets `error.code: conflict`. The app shows the error, with "Review the options and preview again", and nothing is overwritten. | Pass/fail. |
| **X-03 (A5)** | Preview a request, then **Cancel** it in the app. | The request branch is deleted, or, with `pingone-promote/*` protected, the app explains that a Maintainer must delete it (HTTP 403). Cancel is refused if the branch already has the job's generated commit. | Pass/fail each case. Which one applied. |
| **X-04** | After a typed error on Submit (e.g. `preflight` or `conflict`), change any option. | The error clears and Submit is enabled again. If `result.json` had `error.retryable: true`, Submit stays enabled without an edit. If it had `error.field`, the app points at that field. | Pass/fail. |
| **X-05** | Search the `pingone-promote:request` job logs, the MR description, the `result.json` artifact and the DaVinci screens for any secret: client secret, tokens, or `******`-masked values resolved to real ones. | None found. | Pass/fail. |
| **X-06** | Push, by hand, a branch `pingone-promote/test-1` with a `.pingone-promote/request.json` whose `branchName` is `main`, or whose `source` is a rollout env, or with an unknown field. | `pingone-promote:request` refuses it (`status: failed`, `error.type: validation`, with `error.field` naming the field) and commits nothing. It never commits to `main` or to a protected branch outside `pingone-promote/`. | Pass/fail per case. |
| **X-07** | Revert the merged MR from E-06 in GitLab (a normal revert MR). | The revert rolls through the envs again. qa removes the flow. Removing a whole resource means a destroy, so check that the plan says so before you approve. | Plan summary. Pass/fail. |

---

## Cleanup
- Delete the `pingone-promote/*` test branches and close the test MRs.
- Revoke tokens A and B.
- Remove the test SSO group members, if they were temporary.
- Revert any test flows in `dev`/`qa`/`stage`.
- Keep the DaVinci app imported if you plan to continue.

---

## Results form (send this back)

```
Environment:   GitLab tier ____   Terraform version ____   Gate type per env: qa ____ stage ____ prod ____
Settings:      ppApplyJobPattern ____   ppEnvironmentPattern ____   ppRolloutEnvs ____
Flows in dev:  ____

ID     Result (pass/fail/skip)   Timing   Notes (sanitized)
S-1
S-2
S-3
S-4
S-5
S-6
D-01
D-02
D-03
D-04
D-05
D-06
D-07
D-08
D-10
E-01
E-02
E-03
E-04
E-05
E-05b
E-06
E-07
E-08
E-09
E-10
E-11
E-12
E-13
E-14
P-01
P-02
P-03
P-04
P-05
P-06
X-01
X-02
X-03
X-04
X-05
X-06
X-07

Anything surprising:
```

### How to read the results
- **DaVinci is a good home** if all [STOP] tests pass, D-03 (search) answers in a few seconds, and E-02 (preview) completes within your patience, roughly under 3 minutes, which is mostly runner time.
- **DaVinci is not a good home** if sign-in, screens or REST behaviour (D-01, D-03, A2/A3/A5/A6) can't be made to work, or if DaVinci's flow time limits cut off normal use. The fallback is the local app (#75). The engine and CI job stay the same.
