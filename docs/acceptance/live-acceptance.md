# Live acceptance: DaVinci app + GitLab request job

This plan is for you to run in your own environments. Nobody else has access to them.

Each test lists:
- **Do**: what to do
- **Expect**: what should happen
- **Capture**: the evidence to send back

Fill in the [results form](#results-form-send-this-back) at the end and send it back. Evidence must be **sanitized**: no tokens, client secrets, environment IDs, project IDs, user emails or flow contents. Use names, status words, counts and timings instead.

**Stop rule.** If any **[STOP]** test fails, stop there and send the results. Later tests depend on it.

> Tests marked **(engine PR)** depend on the request job from the v2 engine PR. Run groups 0–2 now. Run groups 3–6 after that PR merges. Exact CLI flags are in `docs/gitlab-ci-integration.md` once it lands.

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
| Two GitLab project access tokens on the disposable repo | Both Developer role with `api` scope. Token **A** is for the CI request job. Token **B** is for DaVinci. |

Timing: groups 0–2 take about 1 hour. Groups 3–6 take about 2–3 hours, including waiting on pipelines.

---

## 1. Setup checks

| ID | Do | Expect | Capture |
| --- | --- | --- | --- |
| **S-1 [STOP]** | Follow `davinci-app/README.md` §1–2. Create the SSO app, policy and group. Import `davinci-app/flow/pingone-promote-app.flow.json`. | The import succeeds with no errors. The flow shows about 76 nodes. | Pass/fail. Paste any import error text. |
| **S-2 [STOP]** | Set the company variables `ppWorkerBasicAuth` and `ppGitlabToken` (token **B**). Set the flow variables from README "Configure after import". Use `ppRolloutEnvs` = your real order, e.g. `qa,stage,prod`, and set `ppApplyJobPattern` / `ppEnvironmentPattern` to match your pipeline's job and environment names. | Saved. | The values of `ppApplyJobPattern`, `ppEnvironmentPattern` and `ppRolloutEnvs` (these names are not secret). |
| **S-3** | Open the app before setting one variable, e.g. temporarily blank `ppGitlabProjectId`. | You see "PingOne Promote is not set up yet", the setting **name** is listed, and no value is shown. Restore the variable afterwards. | Screenshot with values blurred. |
| **S-4 (engine PR)** | In the disposable repo: add the CI file and variables from `docs/gitlab-ci-integration.md` (the `.pre` request and check jobs plus the `local:` include). Make `pingone-promote/*` a protected-branch wildcard with Developer push. Add the PingOne worker variables and token **A**, masked and protected. | The MR pipeline adding the file passes `pingone-promote:check`. Enterprise jobs behave as before. | Pipeline status. Job names that ran. |
| **S-5 (engine PR)** | Run the existing pipeline once on `main` without any request. | `pingone-promote:request` does **not** run. Your normal jobs run, and deploys stay manual. | List of jobs that ran. |
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
| **E-01 [STOP] (A7)** | Pick a flow that has a subflow, a connection and a variable. Click **Preview**. | A branch `pingone-promote/<id>` appears in GitLab, containing `.pingone-promote/request.json`. A branch pipeline starts, and `pingone-promote:request` runs in `.pre`. | Seconds until the pipeline appeared. Seconds the job ran. |
| **E-02 [STOP] (A6)** | Wait on the Preview screen. | It shows a list of changes, each one added, updated, kept-differs or unchanged. The selected flow is marked. Blockers per env and any required secrets are listed. | Screenshot of the summary with names only. Total seconds from click to preview. |
| **E-03** | Open the request job log in GitLab. | It shows a pinned build, the terraformer export of **only** the selected flow and its dependencies, and `terraform fmt` passing. No secret values appear in the log. | Pass/fail. Paste any line that looks like a secret, redacted. |
| **E-04** | Check the branch after the preview. | **No** generated commit. Only the request commit is there, because preview never commits. | Pass/fail. |
| **E-05** | Check enterprise jobs in the request branch pipeline. | They behave as on any branch: plan at most, and **no apply runs**, since deploys are manual. | Jobs that ran. Confirm nothing applied. |

---

## 4. Submit, the generated MR, and the additive merge (engine PR)

| ID | Do | Expect | Capture |
| --- | --- | --- | --- |
| **E-06 [STOP]** | Click **Submit** (MR mode). | There is one generated commit on the branch, and `request.json` is deleted in it. An MR to `main` opens with the label `pingone-promote` and the `Requested-by` / `Request-id` lines. The app shows the MR link. | Seconds to MR. MR files-changed count. |
| **E-07 [STOP]** | Review the MR diff. | Only blocks for the selected flow, its **new** dependencies, and the variables and overlay values they need have changed. Every other flow and connection is **unchanged**. Nothing is deleted. | Files changed. Confirm "no unrelated changes" and "no deletions". |
| **E-08** | In the MR, check the root `{env}.tfvars` files, `secrets.tf` and `customer_secrets.yml`. | Each env file gets only new required values. `secrets.tf` gets new secret variables, if any. `customer_secrets.yml` gets new `TF_VAR_` entries at `apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/<field>@secret`, and existing entries and comments are untouched. | Pass/fail per file. |
| **E-09** | The MR pipeline runs `pingone-promote:check` and the enterprise plan jobs. | Check passes. Plans for downstream envs show only the expected adds and changes. | Plan summary per env, as add/change/destroy counts. |
| **E-10** | Before merging, change a **shared** connection used by the flow in dev, then do a new request for the same flow. | The connection shows as **kept-differs** (the Git version is kept). You can switch it to Update, and the default stays Keep. | Pass/fail. |
| **E-11** | Repeat E-06 with **Branch only** mode on another flow. | A branch with one generated commit. No MR. The app shows the branch link and "Not in rollout until merged". | Pass/fail. |
| **E-12** | Submit the same request twice: click Submit again, or use Check GitLab after a refresh. | No second commit and no second MR. The app shows the existing one. | Pass/fail. |
| **E-13** | Run a request for the **same flow again** with no changes in dev. | The preview shows no changes, and submitting produces an empty or no-op result with no new commit. | Pass/fail. |

---

## 5. Rollout and status (engine PR)

| ID | Do | Expect | Capture |
| --- | --- | --- | --- |
| **P-01 [STOP]** | Merge the MR from E-06. | The post-merge pipeline starts on `main`. In the app, the request shows the rollout strip: qa Waiting/Running, the rest Waiting. | Pass/fail. |
| **P-02 (A8)** | Run or approve the qa deploy (manual job or protected-env approval). | qa goes **Needs approval** (with "n of m" if approvals are used), then **Running**, then **Live**. The flow appears in qa. | Seconds per state change in the app versus GitLab. Does "n of m" show? |
| **P-03 (A9)** | Leave stage or prod un-approved. | The strip shows them as **Needs approval** or **Waiting**. | Pass/fail. |
| **P-04** | Check that dev was **not** applied (dev is plan-only). | No apply job ran for dev. | Pass/fail. |
| **P-05** | Make a downstream apply fail on purpose in a safe way, e.g. remove the Vault value for a new secret in `stage`, if you have one. | The app shows **Failed at stage** with the job link, and says later envs were not changed. The plan error lists the missing secret **name** only. | Screenshot. |
| **P-06** | The Requests page. | Your requests are listed with source, rollout chips and MR state. Branch-only requests are marked. | Screenshot with names only. |

---

## 6. Failure handling and security (engine PR)

| ID | Do | Expect | Capture |
| --- | --- | --- | --- |
| **X-01** | Start a Preview, then cancel the GitLab pipeline before the job runs. | After about 2 minutes the app shows "no pipeline yet / Check GitLab". Check GitLab recovers or explains the state. Submit is never stuck disabled. | Pass/fail. |
| **X-02** | Two people (or two browser sessions) request the **same flow** at the same time and both submit. | One succeeds. The other gets a **conflict** with "Regenerate/Preview again". Nothing is overwritten. | Pass/fail. |
| **X-03 (A5)** | Preview a request, then **Cancel** it in the app. | The request branch is deleted. Cancel is refused if the branch has a generated commit. | Pass/fail both cases. |
| **X-04** | Make an edit after an error, e.g. change the branch name after a "branch exists" error. | The error clears and Submit re-enables. | Pass/fail. |
| **X-05** | Search the job logs, MR description, `result.json` artifact and DaVinci screens for any secret: client secret, tokens, `******`-masked values resolved to real ones. | None found. | Pass/fail. |
| **X-06** | Try to push a branch named `pingone-promote/test` containing a request file that targets `main` directly, or with `branchName: main`. | The job refuses: it never commits to the default branch or a protected branch. | Pass/fail. |
| **X-07** | Revert the merged MR from E-06 in GitLab (normal revert MR). | The revert rolls through the envs again. qa removes the flow. Removing a whole resource means a destroy, so check that the plan says so before you approve. | Plan summary. Pass/fail. |

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
E-06
E-07
E-08
E-09
E-10
E-11
E-12
E-13
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
