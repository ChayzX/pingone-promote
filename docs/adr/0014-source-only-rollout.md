# ADR 0014: Source-only rollout

- Status: Accepted
- Date: 2026-11-03
- Issues: #58
- Supersedes: 0003, 0004, 0005, 0007, 0010, 0011 (with 0015)

## Decision

The operator picks a SOURCE environment and the resources to take from it.
There is no target anywhere in the tool. The enterprise pipeline rolls the
SAME commit through the environments in order, one stage per environment:

```
source (e.g. dev)     exported; plan-only drift check; never applied
rollout (qa -> stage -> prod, configurable)
                      per env, same SHA: plan -> gate -> apply
                      terraform plan -var-file=<env>.tfvars
```

- **One root, one overlay per environment.** The config repository holds
  shared definitions (`definitions/`, environment-neutral) and one flat
  overlay per environment, `<env>.tfvars` at the repository root by default
  (the path is a configurable pattern, `{env}.tfvars`). An overlay holds
  `pingone_environment_id`, environment-local values and `import_ids`.
  Secrets never appear in an overlay (ADR 0009).
- **One commit per change.** A change writes definitions, root files,
  overlays, `secrets.tf` and `customer_secrets.yml` together, so every
  environment applies the same definitions.
- **Values for every environment are part of the change.** A change that
  needs a new environment-local value cannot be submitted until every
  environment in the rollout has it ("readiness"). The tool never copies the
  source's value into another environment silently; "the same in every
  environment" is an explicit choice that inlines the literal into the
  shared definitions.
- **Status is three-way.** For each address: live source vs Git head vs a
  base commit (the commit last rolled out to the source, or the last commit
  whose source plan was clean). Statuses: new, changed-in-source, matches,
  git-newer, changed-in-both. Without a base the status is two-way.
- **Gates belong to the pipeline.** Lower environments may apply
  automatically or as manual jobs; prod uses a protected-environment
  approval. The tool only writes Git; it never plans or applies.
- **Per-environment omission** of a resource is out of scope for now.

## Alternatives

- Source-and-target promotion (v1, ADRs 0003 to 0013): rejected. It needed
  a target inventory, per-target diffs and per-target tfvars, while the
  pipeline already promotes one commit through every environment.
- One root folder per environment: kept as fallback A for imports only
  (ADR 0016); not needed.

## Consequences

- The source environment's own overlay is a normal overlay; its
  `import_ids` adopt the resources that already exist there, so the
  source's drift plan imports instead of creating.
- Deleting a resource is never inferred from its absence (ADR 0015).
