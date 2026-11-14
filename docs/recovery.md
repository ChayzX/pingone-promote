# Recovery: roll forward, never revert

## Why not revert

PingOne Promote only adds and updates Terraform definitions. A Git revert of a merged request removes those definitions again, and the next Terraform apply in each environment then sees resources in state that are no longer in the configuration and **destroys them** (applications, populations, policies, flows). Merging again does not bring back what was destroyed. Never revert a request's merge commit or its definition files.

## Fix forward

1. Find the problem (the failed or wrong apply job on the Requests page, or the plan in your pipeline).
2. Make a **new request** from the source environment (correct the source first if needed) that produces the intended state. The merge is additive: existing values that differ are kept in Git unless you choose update for that item in the preview, so choose update for what must change.
3. Review the new merge request and its plan in the enterprise pipeline, then merge. Each environment converges on the corrected definition.
4. If something must really be removed, remove it deliberately in a reviewed commit after reading the plan. Promote itself never deletes: a resource missing from an export is left alone.

If the pipeline stopped part way through the rollout, fix the cause and rerun the failed apply job; do not revert the earlier environments.

## prevent_destroy

Additive merging cannot prove that a future plan is non-destructive: the provider can still propose replacement, or a person can delete a definition. Protect critical resources by hand, in the definitions, with:

```hcl
lifecycle {
  prevent_destroy = true
}
```

Good candidates: populations, applications, the default sign-on and other default policies, identity providers, and anything that holds users or credentials. It is added by hand; the server does not write it. It blocks any plan that would destroy or replace the resource, including a replacement caused by a changed immutable attribute, so the apply fails with an error instead of destroying it. To remove such a resource on purpose, first delete the `lifecycle` setting in its own reviewed commit. Keep the guard in the shared definition so every environment has it.

## Abandoned merge requests

Promote creates a branch (`<branchPrefix><requestId>`) and a merge request. If you decide not to ship it, **close the merge request and delete the branch** in GitLab. Nothing was applied, so nothing else is needed. If the server reported an uncertain commit, the branch is kept on purpose: look at it before deleting. Do not merge an abandoned request later without re-previewing; start a new request instead.

## State per environment

Each environment has its own Terraform state in your enterprise pipeline's backend; Promote never touches state. A merge reaches an environment only when that environment's apply job runs, and the Requests page shows each environment's apply job. A failed rollout therefore leaves the earlier environments applied and the later ones not: fix forward and rerun, and check each environment's state and plan separately.
