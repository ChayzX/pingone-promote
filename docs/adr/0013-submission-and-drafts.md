# ADR 0013: Single-commit GitLab submission and the server-side Draft API

- Status: Accepted
- Date: 2026-11-03
- Issues: #3, #4, #9, #70, #72, #86, #88

## Decision

**One commit per submission.** The environment tfvars, the root
`secrets.tf` and `customer_secrets.yml` are written in ONE Commits API call
with `actions`. `start_branch` creates the branch from the default branch
atomically. Updates carry `last_commit_id`, and the server also compares each
file's current `last_commit_id` with the draft's base before committing. A
mismatch is a typed `ConflictError`, and the fix offered is a rebase.

**Modes.** `mr` (the default) opens a merge request. `branch` pushes the named
branch only, for teams that open MRs another way.

**Safety rails:**
- Branch names follow the Git ref rules. The default branch and protected
  branches are refused.
- An existing branch is never overwritten. If the branch's head carries this
  submission's trailer (`PingOne-Promote-Submission: <id>`), a typed,
  resumable `PartialError` is returned and `resume` finishes the MR without a
  second commit. Otherwise the error is not resumable and the operator picks
  another branch name.
- Every failure before the commit is a `PreflightError`, which is safe to
  retry.
- The project must be private, active and have a protected default branch.
  Redirects are refused.
- Only allow-listed paths are writable: `<environmentsDir>/<env>.tfvars`, the
  `secrets.tf` path and the `customer_secrets.yml` path, all configurable in
  `gitlab.json`. CI files (`*.gitlab-ci.yml`) and dot-directories are never
  writable.
- Every Terraform file passes the real `terraform fmt` (or `tofu fmt`) gate,
  with a configurable `terraformBin`. A missing binary or any difference
  blocks the submission. `customer_secrets.yml` is YAML, so it is validated by
  parsing instead.
- `secrets.tf` is shared by every environment, so it declares the union of
  the secret keys all `environments/*.tfvars` files require.

**Revert (#72)** creates a new branch from the default branch, applies
GitLab's revert of the submission commit, and optionally opens an MR. It
refuses when any file changed after the submission (or the submission is not
merged yet) unless the operator confirms.

**Description (#4).** The MR and commit text carry the per-key change summary
(collection, key, kind, field paths) and the new Vault entries to create.
They never include values.

**Draft API (#70).** A draft is server-side metadata, persisted with
`internal/store`: selections, exclusions, keep-target choices, parameter
values, secret mappings, acknowledgements, base commit IDs and the submission
list. Inventory content and target files stay in memory, so after a restart
a draft reports `needsRebase`. Every PATCH applies typed operations,
recomputes the draft and bumps `revision`; a stale revision returns 409.
Submit errors are typed (`conflict`, `preflight`, `partial`, `blocked`,
`stale_revision`). The routes reuse the loopback security, and every
response is scanned for secrets in tests.

## Alternatives

- One commit per file through the Files API: rejected. A failure midway
  would leave a half-applied change set.
- Re-creating an existing branch: rejected. It can destroy someone's work.
- Running `terraform fmt` only in CI: rejected by the user. The tool must
  never push a file the pipeline formatter would change.

## Consequences

- Submitting requires a terraform or tofu binary on the operator's machine.
- Heuristic secret warnings block submission only for secrets the promoted
  file actually uses.
