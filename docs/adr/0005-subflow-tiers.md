# ADR 0005: Fixed subflow tiers t0..t3

> **Superseded by [ADR 0014](0014-source-only-rollout.md) and [ADR 0015](0015-pingcli-terraformer-exporter.md)** (2026-11-03). Kept for history; the v1 code it describes is deleted in migration step 10.

- Status: Superseded by 0014/0015
- Date: 2026-11-03
- Issues: #60, #63

## Decision

A `for_each` resource cannot reference its own instances, so flows are split
into four resources, `pingone_davinci_flow.t0` to `.t3`. A flow may reference
only flows in **lower** tiers. Tier N resolves placeholders against
`refs_tN`, which contains the IDs of every flow below N.

Tiers are assigned top-down and sticky: top-level flows go in t3, their
subflows in t2, and so on. The validator (`check`) and the module
(`terraform_data.contract_tiers`) both reject a violation and name the flows.

Flow-scoped variables need their flow's ID, so they live in a separate
resource (`pingone_davinci_variable.flow_scoped`) that is created after flows.
Company, flowInstance and user variables (`.global`) are created before flows
so flows can reference them.

## Alternatives

- One flow resource with `depends_on`: impossible, because instances of one
  resource cannot reference each other.
- One resource per flow (generated HCL): rejected. The module must stay fixed
  and be driven only by tfvars.

## Consequences

- Nesting is limited to four levels. Deeper chains need a schema version
  change.
- Raising a flow's tier changes its address, which Terraform plans as destroy
  and create. The app blocks this and points to a `moved` / `terraform state mv`
  runbook (module README).
