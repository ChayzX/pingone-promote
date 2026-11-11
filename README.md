# PingOne Promote

Moves DaVinci configuration from your **dev** environment into Git, so your existing Terraform pipeline can roll it out to the other environments.

```
DaVinci app ──▶ GitLab branch + merge request ──▶ your pipeline: testing ▶ stage ▶ production
(pick a flow)    (the change, as Terraform)        (same commit, manual jobs, prod approval)
```

- You choose **only the source** (dev) and what to bring. You never pick a destination: after the merge, your pipeline's environment jobs apply the same commit to each environment in order.
- Nothing is ever deleted automatically, and the tool never applies anything itself.

---

## Setup: what you do by hand

Do these once, in order. Each step says where to do it.

### Keep these values the same everywhere

Three settings live in two or three places. If they differ, requests fail or never start.

| What | DaVinci company variable | `.gitlab/pingone-promote.yml` | Example |
| --- | --- | --- | --- |
| Source env name and ID | `ppSourceEnvs` | `PINGONE_PROMOTE_SOURCES` (name only) | `development=<env-id>` / `development` |
| Rollout envs, in order | `ppRolloutEnvs` | `PINGONE_PROMOTE_ROLLOUT` | `testing,production` |
| Branch prefix | `ppBranchPrefix` | `PINGONE_PROMOTE_BRANCH_PREFIX` **and** the rule `=~ /^CIAM-/` | `CIAM-` |

Env names must also match your tfvars file names (`testing` means `testing.tfvars`).

### 1. PingOne: a read-only worker

In the PingOne environment where you keep admin apps:
1. Create a **Worker** application.
2. Give it the role **DaVinci Admin Read Only** on the dev environment only.
3. Set its **Token Endpoint Authentication Method** to **Client Secret Basic**.
4. Write down the client ID, the secret, and the ID of the environment that holds the worker.

### 2. GitLab: tokens

On the config repo, under **Settings → Access tokens**, create project access tokens with role **Developer**, scope **`api`**, and a short expiry. Use two tokens: one for the CI job and one for DaVinci.

### 3. Config repo: files

1. Make sure each environment's tfvars sets its environment ID:
   ```hcl
   # development.tfvars, testing.tfvars, production.tfvars
   pingone_environment_id = "<that environment's ID>"
   ```
2. Copy [`examples/gitlab-ci/pingone-promote.yml`](examples/gitlab-ci/pingone-promote.yml) to `.gitlab/pingone-promote.yml`.
3. In `.gitlab-ci.yml`, add one line after the enterprise include:
   ```yaml
   include:
     - project: '<group>/<terraform-pipeline-templates>'   # your existing include
       file: '/<template>.yml'
     - local: '/.gitlab/pingone-promote.yml'               # add this
   ```
4. In `.gitlab/pingone-promote.yml`, set these values:

   | Variable | Set to |
   | --- | --- |
   | `PINGONE_PROMOTE_REPO_URL` | where this tool's source is cloned from |
   | `PINGONE_PROMOTE_REF` | the tool's commit, currently `e3a4b29390f23572a5e5b6373ab48711a8d5727e` |
   | `PINGONE_PROMOTE_RUNNER_TAG` and the `tags:` list | your runner tag |
   | `PINGONE_PROMOTE_SOURCES` | `development` |
   | `PINGONE_PROMOTE_ROLLOUT` | `testing,production` (add `stage` once you have a stage job) |
   | `PINGONE_PROMOTE_BRANCH_PREFIX` | `CIAM-` |
   | the `pingone-promote:request` rule | `$CI_COMMIT_BRANCH =~ /^CIAM-/` |

### 4. GitLab: CI/CD variables

Under **Settings → CI/CD → Variables**, add:

| Variable | Value | Protected | Masked |
| --- | --- | --- | --- |
| `PINGONE_PROMOTE_WORKER_CLIENT_ID` | worker client ID | yes | yes |
| `PINGONE_PROMOTE_WORKER_CLIENT_SECRET` | worker secret | yes | yes |
| `PINGONE_PROMOTE_WORKER_ENVIRONMENT_ID` | env that holds the worker | yes | no |
| `PINGONE_PROMOTE_REGION_CODE` | `NA`, `EU`, `CA`, `AP`, `AU` or `SG` | no | no |
| `PINGONE_PROMOTE_GITLAB_TOKEN` | the CI token from step 2 | yes | yes |

### 5. GitLab: protection

These settings are the real safety controls. Don't skip them.

1. **Request branches** (**Settings → Repository → Protected branches**). Protect the wildcard `CIAM-*`, or `<your prefix>*`. Protected variables only reach protected branches, so the job needs this.
   - **Allowed to push and merge:** only the two token bot users, if your GitLab tier lets you pick users. Otherwise Developers + Maintainers.
   - Why it matters: whoever can push a request branch can change its CI file, and that pipeline sees the protected variables.
2. **`main`:**
   - allow nobody to push;
   - require MR approval;
   - turn on **Prevent approval by author** and **Prevent approvals by users who add commits**;
   - add a `CODEOWNERS` entry for `.gitlab-ci.yml` and `.gitlab/`.
3. **Environments** (**Settings → CI/CD → Protected environments**). Protect each environment. **production** needs an approval.
4. **Settings → CI/CD → General pipelines:** turn on **Prevent outdated deployment jobs**. This stops an older pipeline from rolling prod back.
5. **Label:** create the label `pingone-promote`.

### 6. DaVinci: import the app

1. **DaVinci → Flows → Add Flow → Import from JSON:** [`davinci-app/flow/pingone-promote-app.flow.json`](davinci-app/flow/pingone-promote-app.flow.json).
2. Open the flow and point the nodes at your own **Http**, **Functions** and **Variables** connections.
3. **Flow → Settings:**
   - Flow Timeout **1800 s**;
   - HTTP Response Timeout **15 s**;
   - Logging **Info**. Never use Debug, because it logs tokens.
4. **DaVinci → Variables** (company variables; they survive re-imports). Set these:

   | Variable | Value |
   | --- | --- |
   | `ppWorkerBasicAuth` | `printf '%s:%s' "$ID" "$SECRET" \| base64 \| tr -d '\n'`. One line, with no `Basic ` in front. |
   | `ppGitlabToken` | the DaVinci token from step 2 |
   | `ppWorkerEnvironmentId` | env that holds the worker |
   | `ppSourceEnvs` | `development=<dev env ID>` |
   | `ppRolloutEnvs` | `testing,production` |
   | `ppGitlabApi` | `https://<your gitlab>/api/v4` |
   | `ppGitlabProjectId` | the config repo's numeric ID |
   | `ppBranchPrefix` | `CIAM-` |
   | `ppApplyJobPattern` | the name of your apply jobs with `{env}`, for example `apply:{env}` |
   | `ppEnvironmentPattern` | your GitLab environment names with `{env}`, usually `{env}` |
   | `ppApiOrigin` / `ppAuthOrigin` | only if you are not in North America, for example `https://api.pingone.eu` |

   Everything else has a working default. The full list is in the [DaVinci app reference](davinci-app/README.md#configure-after-import).
5. **Publish** the flow. Publish again after you change any variable.
6. **Launch link:** add a DaVinci **Application** with a **Flow Policy** that runs the flow at 100%. Share its launch link only with the people who should use it, because the app has no sign-in.

### 7. First test

Follow the test cases in [`docs/acceptance/live-acceptance.md`](docs/acceptance/live-acceptance.md). Start with S-1 to S-4, then D-01 to D-03b. D-03b (25 searches in one session) decides whether DaVinci can host the app, so run it early.

---

## Everyday use

1. Open the launch link and type your name.
2. Pick **development**. Search for a flow, or choose **Whole environment**.
3. Choose **Preview**. Fill in anything it asks for: values for each environment, and the secret names that map to Vault.
4. Choose **Submit**. You get a merge request on a `CIAM-…` branch.
5. Get the MR approved and merged.
6. In the merged pipeline, run each environment's apply job in order: **testing**, then **stage** (if you have one), then **production** (with approval). The app's **Rollout** screen shows where the change is live.

## Troubleshooting

| You see | Fix |
| --- | --- |
| The flow times out as soon as it opens | You imported a sign-in build. Import the file from `davinci-app/flow/`. |
| Search: `HTTP 400 … invalid authorization header` | Regenerate `ppWorkerBasicAuth` on one line (step 6.4). Check that the worker uses Client Secret Basic. |
| "PingOne Promote is not set up yet" | Set the variables it names, then publish. |
| Preview waits and says "no pipeline yet" | Check that the branch prefix matches in all three places, that `CIAM-*` is protected, and that the runner tag is right. |
| Job error `must run on branch …` | `ppBranchPrefix` and `PINGONE_PROMOTE_BRANCH_PREFIX` differ. |
| Job error about the terraformer version | `TERRAFORMER_VERSION` in the CI file doesn't match what was built. |
| The app stops with an error after about 20 clicks or checks | DaVinci's limit on how often a node can run in one session (test case D-03b). Reopen the launch link to continue, and report it: this decides whether the app stays in DaVinci. |
| Rollout screen shows no environments | `ppApplyJobPattern` / `ppEnvironmentPattern` don't match your job and environment names. |

## More detail

- [DaVinci app reference](davinci-app/README.md): every setting, the security model, rebuild and checks.
- [GitLab CI reference](docs/gitlab-ci-integration.md): the request file, the job and every variable.
- [Live test cases](docs/acceptance/live-acceptance.md).
- [Architecture decisions](docs/adr/README.md). Start with 0014 to 0017.
- [Developer guide](docs/development.md): build, test, release.
