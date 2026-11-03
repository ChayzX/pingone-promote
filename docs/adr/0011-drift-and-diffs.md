# ADR 0011: Drift and diffs

- Status: Accepted
- Date: 2026-11-03
- Issues: #60, #64

## Decision

Terraform plan is the authority for live effects. The tool keeps three things
of its own:

1. **A per-key tfvars diff** (`tfvars.Diff`), for the UI and the merge
   request description. It lists added, removed and modified keys per
   collection, with field paths that continue into JSON as `/key/...`. JSON
   is compared semantically and sets as sets. Diffs carry paths, never values.
2. **Optimistic concurrency** through GitLab `last_commit_id`: each draft
   records the base commit of every file it writes. Submission compares the
   base before committing and passes `last_commit_id` on updates, and both
   checks map to a typed conflict (ADR 0013).
3. **A per-definition SHA-256** that feeds
   `pingone_davinci_flow_deploy.deploy_trigger_values.definition`. It covers
   the tfvars entry, the resolved node properties (subflow, variable and
   connection IDs, secrets) and the node connection IDs, so a flow redeploys
   when anything it resolves to changes. It is marked nonsensitive so plans
   show redeploys. It is a hash over the whole rendered definition, never
   over a single secret.

## Alternatives

- Calling a live drift API from the app: rejected. It would duplicate plan.

## Consequences

- Whether secret readback causes perpetual diffs is a Phase 0 question.
