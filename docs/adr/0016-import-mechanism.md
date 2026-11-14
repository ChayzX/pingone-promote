# ADR 0016: Adoption with conditional imports on static addresses (spike S1)

- Status: Accepted
- Date: 2026-11-03
- Issues: #58
- Supersedes: 0008

## Context

terraformer's child module declares each resource once, at a static,
non-`for_each` address (`pingone_davinci_flow.pingcli__Login`). One root
serves every environment, and each environment has different IDs, so the
literal `import {}` blocks terraformer writes cannot be committed. The open
question (spike S1) was whether this works on Terraform 1.16:

```hcl
import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.<type>.<label>" }
  to       = module.davinci.<type>.<label>
  id       = "${var.pingone_environment_id}/${each.value}"
}
```

## Spike S1 result: it works

Run offline with `terraform_data` resources inside a child module
(`terraform/spikes/s1-conditional-import`, `terraform test`; the spike is in Git history), on Terraform
**1.16.7** (downloaded from releases.hashicorp.com, SHA-256 checked against
`terraform_1.16.7_SHA256SUMS`) and **1.17.1**:

| Case | Result (both versions) |
| --- | --- |
| zero entries (`import_ids = {}`) | no import; the resource is created |
| one entry | imported with id `<env>/<id>` |
| stale entry after adoption (different ID) | ignored; no change |
| entry removed after adoption | no change |
| key for an address that does not exist | silently ignored |

The pingone provider path was then exercised in the reference root:
`terraform validate` with provider 1.23.0, and `terraform test` with
`mock_provider` and `override_resource` for every imported address, using
the source overlay's `import_ids`.

## Decision

- `imports.tf` at the root holds one conditional import block per managed
  address, generated and extended additively by the merge.
- Each overlay's `import_ids` maps `module.<name>.<type>.<label>` to the ID
  without the environment prefix (the block adds `<environment_id>/`; flow
  policies use `<application_id>/<policy_id>`).
- The source environment's overlay receives the source IDs of newly added
  addresses, so the source's drift plan adopts instead of creating.
- Because Terraform silently ignores unknown keys, `pingone-promote check`
  rejects any `import_ids` key that is not a managed address.
- `required_version >= 1.16.0`.

## Fallbacks (not needed)

- A: a thin root folder per environment around a shared `definitions/`.
- B: convert every resource type to `for_each` in the neutralizer.

## Consequences

- Adopting an environment is a values-only change to its overlay.
- A relabelled address moves its import block and its `import_ids` key
  (`moved {}`, ADR 0015).
